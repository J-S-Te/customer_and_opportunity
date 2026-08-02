// Package requestbody provides fail-closed decoding for HTTP request bodies.
package requestbody

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

const DefaultJSONLimit int64 = 1 << 20

// DecodeJSON decodes one JSON object with the default one MiB request limit.
// It rejects empty bodies, null/array/scalar roots, unknown fields, malformed
// JSON and any non-whitespace content after the first object. Gin's configured
// validator is applied after decoding so binding tags keep their normal
// required/oneof/min/max/dive semantics.
func DecodeJSON(c *gin.Context, target any) error {
	return DecodeJSONWithLimit(c, target, DefaultJSONLimit)
}

// DecodeJSONWithLimit is DecodeJSON with an endpoint-specific positive limit.
func DecodeJSONWithLimit(c *gin.Context, target any, limit int64) error {
	if c == nil || c.Request == nil || c.Request.Body == nil || target == nil || limit <= 0 {
		return errors.New("invalid JSON request")
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	decoder := json.NewDecoder(c.Request.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON object")
		}
		return err
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return errors.New("request body must contain one JSON object")
	}

	objectDecoder := json.NewDecoder(bytes.NewReader(trimmed))
	objectDecoder.DisallowUnknownFields()
	if err := objectDecoder.Decode(target); err != nil {
		return err
	}
	if err := binding.Validator.ValidateStruct(target); err != nil {
		return err
	}
	return nil
}
