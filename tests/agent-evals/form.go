package agentevals

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// The credential fill page (tech design 12.4) is server-rendered HTML that
// a person submits in a browser. The harness plays that person: it opens
// the page with the user's session and posts the form back with the
// values filled in, keeping every hidden field the page sent (for example
// an anti-forgery token). It only reads standard form markup, so it does
// not depend on the page's layout.

// form is a parsed HTML form.
type form struct {
	action string
	method string
	// fields holds the named inputs in page order with their values.
	fields []formField
}

type formField struct {
	name, value, kind string
}

var (
	formTag  = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)</form>`)
	inputTag = regexp.MustCompile(`(?is)<(input|textarea)\b([^>]*)>`)
	attr     = regexp.MustCompile(`(?is)([a-z_:-]+)\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
)

func attrs(s string) map[string]string {
	out := map[string]string{}
	for _, m := range attr.FindAllStringSubmatch(s, -1) {
		v := m[3] + m[4] + m[5]
		out[strings.ToLower(m[1])] = html.UnescapeString(v)
	}
	return out
}

func parseForm(page string) (form, error) {
	m := formTag.FindStringSubmatch(page)
	if m == nil {
		return form{}, errors.New("no form on the page")
	}
	a := attrs(m[1])
	f := form{action: a["action"], method: strings.ToUpper(a["method"])}
	if f.method == "" {
		f.method = http.MethodGet
	}
	for _, in := range inputTag.FindAllStringSubmatch(m[2], -1) {
		ia := attrs(in[2])
		if ia["name"] == "" {
			continue
		}
		kind := ia["type"]
		if strings.EqualFold(in[1], "textarea") {
			kind = "textarea"
		}
		f.fields = append(f.fields, formField{name: ia["name"], value: ia["value"], kind: strings.ToLower(kind)})
	}
	return f, nil
}

// fetchForm opens a fill page with the user's session.
func fetchForm(ctx context.Context, in *Instance, pageURL string) (form, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return form{}, err
	}
	resp, err := in.Session.Do(req)
	if err != nil {
		return form{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return form{}, fmt.Errorf("GET %s = %d", pageURL, resp.StatusCode)
	}
	return parseForm(string(body))
}

// FillCredential submits a fill page as the user: each named field in
// values gets its value, hidden fields keep theirs.
func FillCredential(ctx context.Context, in *Instance, pageURL string, values map[string]string) error {
	f, err := fetchForm(ctx, in, pageURL)
	if err != nil {
		return err
	}
	data := url.Values{}
	filled := map[string]bool{}
	for _, fld := range f.fields {
		if v, ok := values[fld.name]; ok {
			data.Set(fld.name, v)
			filled[fld.name] = true
			continue
		}
		switch fld.kind {
		case "submit", "button", "checkbox", "radio":
			continue
		}
		data.Set(fld.name, fld.value)
	}
	for name := range values {
		if !filled[name] {
			return fmt.Errorf("the fill page has no field %s", name)
		}
	}
	target, err := url.Parse(pageURL)
	if err != nil {
		return err
	}
	if f.action != "" {
		if target, err = target.Parse(f.action); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", in.Base)
	resp, err := in.Session.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s = %d: %s", target, resp.StatusCode, body)
	}
	return nil
}
