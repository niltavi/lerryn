package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Headers of generic webhook requests (contract, webhooks.notification).
const (
	HeaderEvent     = "X-Lerryn-Event"
	HeaderDelivery  = "X-Lerryn-Delivery"
	HeaderSignature = "X-Lerryn-Signature"
)

// SignatureTolerance is how far the signature time may be from the
// receiver's clock; receivers should reject older requests.
const SignatureTolerance = 5 * time.Minute

const userAgent = "Lerryn-Notify/1"

// Request is the body and headers of one outgoing notification.
type Request struct {
	Body   []byte
	Header http.Header
}

// Build renders e for a channel of kind. secret is the signing secret of
// generic webhooks and ignored for the bot formats; now is the signature
// time; loc is the time zone of times in the text.
func Build(kind string, e Event, secret string, now time.Time, loc *time.Location) (Request, error) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", userAgent)
	var (
		body []byte
		err  error
	)
	switch kind {
	case KindWebhook:
		if secret == "" {
			return Request{}, errors.New("notify: webhook channel without a signing secret")
		}
		body, err = json.Marshal(e)
		if err != nil {
			return Request{}, fmt.Errorf("encode notification event: %w", err)
		}
		h.Set(HeaderEvent, e.Type)
		h.Set(HeaderDelivery, e.ID.String())
		h.Set(HeaderSignature, Sign(secret, now, body))
	case KindFeishu:
		body, err = json.Marshal(feishuBody(Compose(e, loc)))
	case KindSlack:
		body, err = json.Marshal(slackBody(Compose(e, loc)))
	default:
		return Request{}, fmt.Errorf("notify: unknown channel kind %q", kind)
	}
	if err != nil {
		return Request{}, fmt.Errorf("encode %s message: %w", kind, err)
	}
	return Request{Body: body, Header: h}, nil
}

// Sign returns the X-Lerryn-Signature value for body sent at t:
// t=<Unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>.
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + signature(secret, ts, body)
}

func signature(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks an X-Lerryn-Signature header the way a receiver should:
// the signature matches and its time is within SignatureTolerance of now.
// Several v1 values are accepted, so that a future secret rotation can
// sign with both secrets for a while.
func Verify(secret, header string, body []byte, now time.Time) error {
	var (
		ts   string
		sigs []string
	)
	for part := range strings.SplitSeq(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return errors.New("notify: malformed signature header")
	}
	if d := now.Sub(time.Unix(sec, 0)); d > SignatureTolerance || d < -SignatureTolerance {
		return errors.New("notify: signature time outside the tolerance")
	}
	want := signature(secret, ts, body)
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			return nil
		}
	}
	return errors.New("notify: signature mismatch")
}

// feishuBody is a Feishu custom bot "post" message: a title, one line per
// field and a link.
func feishuBody(m Message) map[string]any {
	type node = map[string]string
	var lines [][]node
	for _, f := range m.Fields {
		lines = append(lines, []node{{"tag": "text", "text": f.Label + "：" + f.Value}})
	}
	if m.Note != "" {
		lines = append(lines, []node{{"tag": "text", "text": m.Note}})
	}
	if m.Link != "" {
		lines = append(lines, []node{{"tag": "a", "text": m.LinkText, "href": m.Link}})
	}
	return map[string]any{
		"msg_type": "post",
		"content": map[string]any{
			"post": map[string]any{
				"zh_cn": map[string]any{"title": m.Title, "content": lines},
			},
		},
	}
}

// slackBody is a Slack incoming webhook message: mrkdwn text in one
// section block, with the same text as the notification fallback.
func slackBody(m Message) map[string]any {
	var b strings.Builder
	b.WriteString("*" + slackEscape(m.Title) + "*")
	for _, f := range m.Fields {
		b.WriteString("\n" + slackEscape(f.Label) + "：" + slackEscape(f.Value))
	}
	if m.Note != "" {
		b.WriteString("\n" + slackEscape(m.Note))
	}
	if m.Link != "" {
		b.WriteString("\n<" + m.Link + "|" + slackEscape(m.LinkText) + ">")
	}
	text := b.String()
	return map[string]any{
		"text": text,
		"blocks": []any{
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
		},
	}
}

// slackEscape escapes the three characters Slack's mrkdwn reserves, so a
// task name cannot inject links or mentions.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
