// Command presale-integration-mock 提供本地受控的审批引擎与 PMS 联调桩。
// 它只用于开发/联调环境，不承载任何真实业务规则；生产对接使用正式服务时不得启动。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const maxBodyBytes = 1 << 20

type envelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Data      any    `json:"data,omitempty"`
	Details   any    `json:"details,omitempty"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

type clientCredential struct {
	id     string
	secret string
}

func main() {
	address := env("MOCK_ADDRESS", ":8092")
	approvalClient := clientCredential{id: env("MOCK_APPROVAL_CLIENT_ID", "presale-mock-approval"), secret: env("MOCK_APPROVAL_CLIENT_SECRET", "presale-mock-approval-secret")}
	pmsClient := clientCredential{id: env("MOCK_PMS_CLIENT_ID", "presale-mock-pms"), secret: env("MOCK_PMS_CLIENT_SECRET", "presale-mock-pms-secret")}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, envelope{Code: "OK", Message: "ok", RequestID: newRequestID(), Data: map[string]string{"status": "up"}})
	})
	mux.HandleFunc("/oauth2/token", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeEnvelopeError(writer, http.StatusMethodNotAllowed, "MOCK_METHOD_NOT_ALLOWED", "method not allowed", nil)
			return
		}
		if err := request.ParseForm(); err != nil {
			writeEnvelopeError(writer, http.StatusBadRequest, "MOCK_BAD_FORM", "invalid form", nil)
			return
		}
		clientID, clientSecret, ok := request.BasicAuth()
		if !ok {
			clientID = request.Form.Get("client_id")
			clientSecret = request.Form.Get("client_secret")
		}
		scope := strings.TrimSpace(request.Form.Get("scope"))
		if request.Form.Get("grant_type") != "client_credentials" || !authorize(clientID, clientSecret, approvalClient, pmsClient) {
			writeEnvelopeError(writer, http.StatusUnauthorized, "MOCK_BAD_CREDENTIALS", "invalid client credentials", nil)
			return
		}
		writeJSON(writer, http.StatusOK, tokenResponse{
			AccessToken: "mock-token-" + randomHex(24),
			TokenType:   "Bearer",
			ExpiresIn:   3600,
			Scope:       scope,
		})
	})
	mux.HandleFunc("/approval/instances", func(writer http.ResponseWriter, request *http.Request) {
		if !requireBearer(writer, request) {
			return
		}
		var payload struct {
			EventID string `json:"event_id"`
		}
		if !decodeBody(writer, request, &payload) {
			return
		}
		if payload.EventID == "" {
			writeEnvelopeError(writer, http.StatusUnprocessableEntity, "MOCK_VALIDATION_ERROR", "event_id is required", nil)
			return
		}
		writeJSON(writer, http.StatusCreated, envelope{Code: "OK", Message: "ok", RequestID: newRequestID(), Data: map[string]any{
			"engine_instance_id": "MOCK-INSTANCE-" + randomHex(24),
			"event_sequence":     1,
			"next_approver_id":   env("MOCK_APPROVAL_NEXT_APPROVER_ID", "mock-approver-user"),
			"next_approver_name": env("MOCK_APPROVAL_NEXT_APPROVER_NAME", "模拟审批人"),
		}})
	})
	mux.HandleFunc("/approval/actions", func(writer http.ResponseWriter, request *http.Request) {
		if !requireBearer(writer, request) {
			return
		}
		var payload struct {
			EngineTaskID string `json:"engine_task_id"`
		}
		if !decodeBody(writer, request, &payload) {
			return
		}
		if payload.EngineTaskID == "" {
			writeEnvelopeError(writer, http.StatusUnprocessableEntity, "MOCK_VALIDATION_ERROR", "engine_task_id is required", nil)
			return
		}
		writeJSON(writer, http.StatusAccepted, envelope{Code: "OK", Message: "ok", RequestID: newRequestID(), Data: map[string]any{
			"engine_task_id": payload.EngineTaskID,
			"accepted":       true,
		}})
	})
	mux.HandleFunc("/pms/worklogs", func(writer http.ResponseWriter, request *http.Request) {
		if !requireBearer(writer, request) {
			return
		}
		var payload struct {
			WorklogID string `json:"worklogId"`
		}
		if !decodeBody(writer, request, &payload) {
			return
		}
		if payload.WorklogID == "" {
			writeEnvelopeError(writer, http.StatusUnprocessableEntity, "MOCK_VALIDATION_ERROR", "worklogId is required", nil)
			return
		}
		writeJSON(writer, http.StatusAccepted, envelope{Code: "OK", Message: "ok", RequestID: newRequestID(), Data: map[string]any{
			"worklog_id":   payload.WorklogID,
			"receipt_code": "MOCK-RECEIPT-" + randomHex(24),
		}})
	})

	log.Printf("presale integration mock listening on %s", address)
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second}
	log.Fatal(server.ListenAndServe())
}

func authorize(clientID, clientSecret string, pairs ...clientCredential) bool {
	for _, pair := range pairs {
		if pair.id != "" && pair.secret != "" && clientID == pair.id && clientSecret == pair.secret {
			return true
		}
	}
	return false
}

func requireBearer(writer http.ResponseWriter, request *http.Request) bool {
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(authorization), "bearer ") || len(strings.Fields(authorization)) != 2 {
		writeEnvelopeError(writer, http.StatusUnauthorized, "MOCK_UNAUTHENTICATED", "bearer token is required", nil)
		return false
	}
	return true
}

func decodeBody(writer http.ResponseWriter, request *http.Request, target any) bool {
	if request.Method != http.MethodPost {
		writeEnvelopeError(writer, http.StatusMethodNotAllowed, "MOCK_METHOD_NOT_ALLOWED", "method not allowed", nil)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeEnvelopeError(writer, http.StatusUnprocessableEntity, "MOCK_VALIDATION_ERROR", "invalid JSON body", nil)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeEnvelopeError(writer, http.StatusUnprocessableEntity, "MOCK_VALIDATION_ERROR", "body must contain a single JSON object", nil)
		return false
	}
	return true
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("X-Request-ID", newRequestID())
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeEnvelopeError(writer http.ResponseWriter, status int, code, message string, details any) {
	writeJSON(writer, status, envelope{Code: code, Message: message, RequestID: newRequestID(), Details: details})
}

func newRequestID() string {
	return "MOCK-" + randomHex(24)
}

func randomHex(bytes int) string {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
