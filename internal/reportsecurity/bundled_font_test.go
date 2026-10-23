package reportsecurity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/font"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portalreportreference"
)

type bundledFontIdentity struct{}

func (bundledFontIdentity) Resolve(_ context.Context, tenant string, customer uint64, account string) (portalreportreference.Identity, error) {
	return portalreportreference.Identity{TenantID: tenant, CustomerID: customer, AccountID: account, CustomerName: "测试客户单位有限公司 ABC-2026", AccountMasked: "13*******90"}, nil
}

// 已入库的字体即生产资源，不能以操作系统安装的字体替代。
// 设置 UIP_TEST_PDF_RENDER_REQUIRED=1 时，原本可选的 Poppler 执行变为必需。
func TestBundledPortalFontRealChinesePDFWatermark(t *testing.T) {
	path, err := filepath.Abs("../../assets/fonts/UIPPortalSans-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	metadataBytes, err := os.ReadFile(filepath.Join(filepath.Dir(path), "SOURCE.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		StaticSHA256   string `json:"static_sha256"`
		PostScriptName string `json:"postscript_name"`
		StaticSize     int64  `json:"static_size_bytes"`
	}
	if err = json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	if hex.EncodeToString(hash[:]) != metadata.StaticSHA256 || metadata.StaticSize != int64(len(body)) || metadata.PostScriptName != "UIPPortalSans-Regular" {
		t.Fatal("production font metadata or SHA256 mismatch")
	}
	raw := pdfFixture(t)
	watermarker, err := NewPDFWatermarker(t.TempDir(), path, bundledFontIdentity{}, numberFixture{})
	if err != nil {
		t.Fatalf("real bundled TTF constructor: %v", err)
	}
	if watermarker.fontName != "UIPPortalSans-Regular" {
		t.Fatal("basename and PostScript name differ")
	}
	stamp := report.WatermarkContext{TenantID: "qa-font-tenant", CustomerID: 42, RequestID: 123, AccountID: "qa-font-account", TrackingCode: "tracking-20261013-0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ", DownloadedAt: time.Date(2026, 10, 13, 7, 4, 5, 0, time.UTC)}
	expected := []string{"客户：测试客户单位有限公司 ABC-2026", "账号：13*******90", "报告申请：REPORT-123", "下载时间：2026-10-13T15:04:05+08:00", "追踪：" + stamp.TrackingCode}
	metrics, ok := font.UserFontMetrics[watermarker.fontName]
	if !ok || metrics.PostscriptName != metadata.PostScriptName {
		t.Fatal("bundled font metrics missing")
	}
	for _, line := range expected {
		for _, r := range line {
			glyph, ok := metrics.Chars[uint32(r)]
			if !ok || glyph == 0 || metrics.ToUnicode[glyph] != uint32(r) {
				t.Fatalf("watermark contains missing glyph U+%04X", r)
			}
		}
	}
	for r := rune(32); r <= 126; r++ {
		if glyph, ok := metrics.Chars[uint32(r)]; !ok || glyph == 0 {
			t.Fatalf("printable ASCII missing U+%04X", r)
		}
	}
	stamped, err := watermarker.Apply(context.Background(), raw, stamp)
	if err != nil {
		t.Fatalf("real bundled font Apply: %v", err)
	}
	if bytes.Equal(stamped, raw) || api.Validate(bytes.NewReader(stamped), watermarker.conf) != nil {
		t.Fatal("invalid or unchanged stamped PDF")
	}
	if count, err := api.PageCount(bytes.NewReader(stamped), watermarker.conf); err != nil || count != 1 {
		t.Fatal("watermark changed pagination", err)
	}
	t.Run("independent_poppler_text_and_render", func(t *testing.T) {
		tools := map[string]string{}
		for _, name := range []string{"pdftotext", "pdftoppm", "pdffonts"} {
			path, err := exec.LookPath(name)
			if err != nil {
				if os.Getenv("UIP_TEST_PDF_RENDER_REQUIRED") == "1" {
					t.Fatalf("required independent Poppler tool %s unavailable", name)
				}
				t.Skip("optional independent Poppler tooling unavailable; use UIP_TEST_PDF_RENDER_REQUIRED=1 for release verification")
			}
			tools[name] = path
		}
		dir := t.TempDir()
		pdf := filepath.Join(dir, "portal-bundled-font-sample.pdf")
		if err := os.WriteFile(pdf, stamped, 0600); err != nil {
			t.Fatal(err)
		}
		run := func(name string, args ...string) []byte {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, tools[name], args...).CombinedOutput()
			if err != nil {
				t.Fatalf("independent %s: %v: %s", name, err, out)
			}
			return out
		}
		text := string(run("pdftotext", "-enc", "UTF-8", "-layout", pdf, "-"))
		compact := func(value string) string {
			return strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, value)
		}
		if strings.ContainsRune(text, '\uFFFD') || strings.ContainsRune(text, '\u25A1') {
			t.Fatal("Poppler extracted replacement/missing-glyph character")
		}
		for _, line := range expected {
			if !strings.Contains(compact(text), compact(line)) {
				t.Fatalf("independent extraction lost full watermark field %q: %q", line, text)
			}
		}
		listed := string(run("pdffonts", pdf))
		if !strings.Contains(listed, "UIPPortalSans-Regular") || !strings.Contains(listed, "CID TrueType") || !strings.Contains(listed, "yes") {
			t.Fatalf("expected embedded TrueType font: %s", listed)
		}
		png := filepath.Join(dir, "portal-bundled-font-render")
		run("pdftoppm", "-f", "1", "-singlefile", "-r", "144", "-png", pdf, png)
		rendered, err := os.Open(png + ".png")
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(rendered)
		closeErr := rendered.Close()
		if err != nil || closeErr != nil {
			t.Fatal("independent render invalid", err, closeErr)
		}
		bounds := img.Bounds()
		if bounds.Dx() < 1000 || bounds.Dy() < 1500 {
			t.Fatal("unexpected independent A4 render size")
		}
		ink := 0
		for y := bounds.Max.Y - 250; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				if r < 60000 || g < 60000 || b < 60000 {
					ink++
				}
			}
		}
		if ink < 1000 {
			t.Fatalf("independent render has insufficient visible watermark ink: %d", ink)
		}
		if export := os.Getenv("UIP_PORTAL_FONT_QA_OUTPUT"); export != "" {
			if !filepath.IsAbs(export) || !strings.HasPrefix(export, "/private/tmp/uip-portal-font-") {
				t.Fatal("font QA export must use its own /private/tmp/uip-portal-font-* directory")
			}
			if err := os.MkdirAll(export, 0700); err != nil {
				t.Fatal(err)
			}
			pngBytes, err := os.ReadFile(png + ".png")
			if err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string][]byte{"portal-bundled-font-sample.pdf": stamped, "portal-bundled-font-render.png": pngBytes, "portal-bundled-font-text.txt": []byte(text), "portal-bundled-font-pdffonts.txt": []byte(listed)} {
				if err := os.WriteFile(filepath.Join(export, name), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		t.Logf("Poppler complete Chinese/ASCII fields; rendered %dx%d; bottom watermark ink pixels=%d", bounds.Dx(), bounds.Dy(), ink)
	})
}
