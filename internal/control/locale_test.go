package control

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"rillway/internal/config"
	"rillway/internal/i18n"
	"rillway/internal/outbound"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLocaleCatalogCompleteness(t *testing.T) {
	data, err := assets.ReadFile("web/locales.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalogs map[string]map[string]string
	if err := json.Unmarshal(data, &catalogs); err != nil {
		t.Fatal(err)
	}
	en, zh := catalogs["en"], catalogs["zh-Hant"]
	if len(en) == 0 || len(en) != len(zh) || len(catalogs) != 2 {
		t.Fatal("locale catalogs have different keys")
	}
	tpl := regexp.MustCompile(`\{\w+\}`)
	for source, original := range en {
		translated, ok := zh[source]
		if original != source || !ok || translated == "" {
			t.Fatalf("incomplete message %q", source)
		}
		if strings.HasPrefix(source, "[data-") || source == "td" || source == "tr" {
			t.Fatalf("implementation text included in catalog: %q", source)
		}
		a, b := tpl.FindAllString(source, -1), tpl.FindAllString(translated, -1)
		sort.Strings(a)
		sort.Strings(b)
		if strings.Join(a, ",") != strings.Join(b, ",") {
			t.Fatalf("translation lost placeholders: %q", source)
		}
	}
	markup, _ := assets.ReadFile("web/index.html")
	static := regexp.MustCompile(`>([^<>]+)<|(?:aria-label|placeholder|title)="([^"]+)"`)
	for _, match := range static.FindAllStringSubmatch(string(markup), -1) {
		source := strings.TrimSpace(html.UnescapeString(match[1] + match[2]))
		if source != "" {
			if _, ok := en[source]; !ok {
				t.Fatalf("static UI message missing from catalog: %q", source)
			}
		}
	}
	messageCall := regexp.MustCompile(`(?:^|[^\w$])(?:t|et|notice|sourceError)\('([^']*)'`)
	for _, file := range []string{"web/app.js", "web/i18n.js"} {
		source, _ := assets.ReadFile(file)
		for _, match := range messageCall.FindAllStringSubmatch(string(source), -1) {
			if _, ok := en[match[1]]; !ok {
				t.Fatalf("dynamic UI message missing: %q", match[1])
			}
		}
	}
}

func TestLocalizedAPIErrorsPreserveOnlySafeSource(t *testing.T) {
	const source = "Enter a valid management token to sign in."
	b := newBackend()
	server := httptest.NewServer(New(b, "correct"))
	defer server.Close()
	client, err := NewClient(server.URL, "wrong", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []i18n.Locale{i18n.English, i18n.TraditionalChinese} {
		_, err := client.Config(i18n.WithLocale(context.Background(), locale))
		var apiError *APIError
		if !errors.As(err, &apiError) || apiError.Source != source || apiError.Message != i18n.Message(locale, source) {
			t.Fatalf("localized client error: %#v %v", apiError, err)
		}
	}
	b.errorAction = config.PublicError{Message: "Enter a WARP+ license key.", Err: errors.New("private-key-must-not-leak")}
	r := httptest.NewRequest("POST", "/api/v1/outbounds/warp/connect", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer correct")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept-Language", "zh-Hant")
	w := httptest.NewRecorder()
	New(b, "correct").ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), "private-key-must-not-leak") || !strings.Contains(w.Body.String(), `"error_source":"Enter a WARP+ license key."`) || w.Header().Get("Content-Language") != "zh-Hant" {
		t.Fatalf("unsafe or unlocalized API response: %s", w.Body.String())
	}
}

func TestLocalizedStatusDoesNotRewriteUserData(t *testing.T) {
	const source = "userspace interface; handshake requires actual traffic"
	const unknown = "公司 😀 <custom upstream status>"
	b := newBackend()
	b.statuses = []outbound.Status{{ID: "公司 🛰️", Detail: source}, {ID: "unknown", Detail: unknown}}
	r := httptest.NewRequest("GET", "/api/v1/outbounds", nil)
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("Accept-Language", "zh-TW, en;q=0.8")
	w := httptest.NewRecorder()
	New(b, "token").ServeHTTP(w, r)
	var statuses []struct {
		ID           string `json:"id"`
		Detail       string `json:"detail"`
		DetailSource string `json:"detail_source"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &statuses); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0].ID != b.statuses[0].ID || statuses[0].Detail != i18n.Message(i18n.TraditionalChinese, source) || statuses[0].DetailSource != source || statuses[1].Detail != unknown {
		t.Fatalf("unexpected localized statuses: %+v", statuses)
	}
	if b.statuses[0].Detail != source {
		t.Fatal("localization mutated backend data")
	}
}

func TestLanguageControlsAndOfflineFonts(t *testing.T) {
	h := New(newBackend(), "token")
	for _, path := range []string{"/i18n.js", "/locales.json"} {
		if w := request(h, "GET", path, "", "", ""); w.Code != 200 {
			t.Fatalf("missing localization asset: %s", path)
		}
	}
	markup, _ := assets.ReadFile("web/index.html")
	for _, id := range []string{"login-language", "sidebar-language"} {
		if !strings.Contains(string(markup), `id="`+id+`" data-locale`) || !strings.Contains(string(markup), `for="`+id+`"`) {
			t.Fatalf("missing accessible language selector: %s", id)
		}
	}
	for _, path := range []string{"/fonts/NotoSansTC.ttf", "/fonts/NotoColorEmoji.ttf"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Range", "bytes=0-3")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusPartialContent || w.Header().Get("Content-Type") != "font/ttf" || w.Body.Len() != 4 || w.Header().Get("Cache-Control") != "public, max-age=3600" {
			t.Fatalf("invalid font asset: %s %d %v", path, w.Code, w.Header())
		}
		head := request(h, "HEAD", path, "", "", "")
		length, _ := strconv.Atoi(head.Header().Get("Content-Length"))
		if head.Code != http.StatusOK || length < 1000 || head.Body.Len() != 0 {
			t.Fatal("invalid font HEAD response")
		}
	}
	css, _ := assets.ReadFile("web/app.css")
	if strings.Contains(string(css), "overflow-x:hidden") || !strings.Contains(string(css), ".sr-only{position:absolute;left:0;top:0;") {
		t.Fatal("mobile table overflow must be fixed at its source")
	}
}

func TestBrowserLocaleStateContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable; run the browser locale contract test where Node is installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, node, "testdata/locale_test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("browser locale contract: %v\n%s", err, output)
	}
}
