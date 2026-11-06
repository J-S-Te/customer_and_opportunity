package requestbody

import (
	"errors"
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

// nestedFixture 覆盖嵌套对象与对象数组：重复键只在同一对象内算重复。
type nestedFixture struct {
	Items []validationFixture `json:"items" binding:"required,min=1,dive"`
	Meta  map[string]string   `json:"meta"`
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
		"duplicate key":    `{"name":"legal","name":"other","kind":"A"}`,
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

func TestDecodeJSONRejectsDuplicateObjectKeys(t *testing.T) {
	// 顶层、嵌套对象和对象数组元素内的重复键都必须被拒绝；数组里不同元素的同名键不是重复。
	tests := map[string]string{
		"top level":       `{"name":"a","name":"b","kind":"A"}`,
		"nested in array": `{"items":[{"name":"a","name":"b","kind":"A"}],"meta":{}}`,
		"nested in map":   `{"items":[{"name":"a","kind":"A"}],"meta":{"x":"1","x":"2"}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			context := jsonTestContext(body)
			var value nestedFixture
			err := DecodeJSON(context, &value)
			if err == nil || !errors.Is(err, ErrDuplicateJSONKey) {
				t.Fatalf("DecodeJSON() error = %v, want %v", err, ErrDuplicateJSONKey)
			}
		})
	}
}

func TestDecodeJSONAcceptsRepeatedKeysAcrossDistinctObjects(t *testing.T) {
	context := jsonTestContext(`{"items":[{"name":"a","kind":"A"},{"name":"b","kind":"B"}],"meta":{"x":"1","y":"2"}}`)
	var value nestedFixture
	if err := DecodeJSON(context, &value); err != nil {
		t.Fatalf("DecodeJSON() error = %v", err)
	}
	if len(value.Items) != 2 || value.Items[0].Name != "a" || value.Items[1].Kind != "B" || value.Meta["y"] != "2" {
		t.Fatalf("decoded value = %#v", value)
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
