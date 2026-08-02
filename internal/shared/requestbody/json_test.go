package requestbody

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type validationFixture struct {
	Name string `json:"name" binding:"required,max=8"`
	Kind string `json:"kind" binding:"required,oneof=A B"`
}

func TestDecodeJSONAcceptsOneValidatedObject(t *testing.T) {
	context := jsonTestContext(`{"name":"legal","kind":"A"}`)
	var value validationFixture
	if err := DecodeJSON(context, &value); err != nil {
		t.Fatalf("DecodeJSON() error = %v", err)
	}
	if value.Name != "legal" || value.Kind != "A" {
		t.Fatalf("decoded value = %#v", value)
	}
}

func TestDecodeJSONRejectsUnsafeOrInvalidBodies(t *testing.T) {
	tests := map[string]string{
		"empty":         "",
		"null":          "null",
		"array":         `[{"name":"legal","kind":"A"}]`,
		"unknown field": `{"name":"legal","kind":"A","secret":"unexpected"}`,
		"trailing value": `{"name":"legal","kind":"A"}
{"name":"second","kind":"B"}`,
		"missing required": `{"kind":"A"}`,
		"invalid oneof":    `{"name":"legal","kind":"C"}`,
		"over max":         `{"name":"too-long-name","kind":"A"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			context := jsonTestContext(body)
			var value validationFixture
			if err := DecodeJSON(context, &value); err == nil {
				t.Fatal("DecodeJSON() error = nil")
			}
		})
	}
}

func TestDecodeJSONRejectsBodyOverLimit(t *testing.T) {
	context := jsonTestContext(`{"name":"legal","kind":"A","padding":"` + strings.Repeat("x", 128) + `"}`)
	var value validationFixture
	err := DecodeJSONWithLimit(context, &value, 64)
	if err == nil || !strings.Contains(err.Error(), "request body too large") {
		t.Fatalf("DecodeJSONWithLimit() error = %v", err)
	}
}

func jsonTestContext(body string) *gin.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	context.Request = request
	return context
}
