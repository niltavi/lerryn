package web

import (
	_ "embed"
	"html/template"
	"strings"

	"github.com/niltavi/lerryn/design/brand"
)

// The page follows design/prototype/Credential-Fill.dc.html and
// Credential-States.dc.html in plain HTML and CSS. The colours, sizes and
// radii come from the same generated Niltavi tokens as the console;
// the dark theme follows the system.

//go:embed theme.css
var pageTheme string

var pageTemplate = template.Must(template.New("fill").Funcs(template.FuncMap{
	"theme": func() template.CSS {
		return template.CSS(pageTheme) // #nosec G203 -- generated CSS embedded from the repository
	},
	"logo": func() template.HTML {
		// The master logo file, embedded at build time.
		return template.HTML(strings.TrimSpace(brand.Mark)) // #nosec G203 -- a trusted file from the repository
	},
}).Parse(pageHTML))

const pageHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta name="referrer" content="same-origin">
<title>{{if eq .State "login"}}登录{{else if eq .State "form"}}填写凭证{{else}}凭证填写{{end}} · Lerryn</title>
<style nonce="{{.Nonce}}">
{{theme}}
*, *::before, *::after { box-sizing: border-box; }
html, body { margin: 0; }
body { min-height: 100vh; background: var(--paper); color: var(--ink); font-family: var(--font-sans); font-size: 16px; line-height: 24px; -webkit-font-smoothing: antialiased; }
a { color: var(--kraft-strong); text-decoration: none; font-weight: 600; }
a:hover { text-decoration: underline; }
:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
svg.i { flex: none; fill: none; stroke: currentColor; stroke-width: 2; stroke-linecap: round; stroke-linejoin: round; }
.auth { min-height: 100vh; display: flex; flex-direction: column; align-items: center; justify-content: center; padding: 48px 16px; }
.col { width: 100%; max-width: 480px; display: flex; flex-direction: column; gap: 20px; }
.col--narrow { max-width: 400px; }
.spread { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px; }
.stack { display: flex; flex-direction: column; gap: 14px; }
.center { align-items: center; text-align: center; }
.logo { width: 44px; height: 44px; border-radius: var(--radius-2xl); background: var(--plate); display: grid; place-items: center; }
.logo svg { display: block; width: 32px; height: auto; }
.logo--lg { width: 64px; height: 64px; border-radius: var(--radius-4xl); }
.logo--lg svg { width: 46px; }
.who { display: flex; align-items: center; gap: 8px; font-size: 14px; line-height: 20px; }
.avatar { width: 24px; height: 24px; border-radius: 9999px; background: var(--kraft-soft); color: var(--kraft-strong); display: grid; place-items: center; font-size: 11px; font-weight: 700; }
.card { background: var(--liner); border: 1px solid var(--line); border-radius: var(--radius-3xl); box-shadow: var(--flute-b); padding: 28px; }
.card .stack { gap: 20px; }
.g { width: 44px; height: 44px; border-radius: var(--radius-2xl); display: grid; place-items: center; flex: none; }
.g svg.i { width: 22px; height: 22px; stroke-width: 2.5; }
.g--key { background: var(--kraft-soft); color: var(--kraft-strong); }
.g--ok { background: var(--success-soft); color: var(--success); }
.g--idle { background: var(--liner-2); color: var(--ink-muted); }
h1 { margin: 0; font-size: 24px; line-height: 30px; font-weight: 700; letter-spacing: -0.01em; overflow-wrap: anywhere; }
h2 { margin: 0; font-size: 20px; line-height: 28px; font-weight: 600; }
p { margin: 0; }
.muted { color: var(--ink-muted); }
.small { font-size: 14px; line-height: 20px; }
.mono { font-family: var(--font-mono); font-size: 13px; line-height: 20px; font-weight: 600; overflow-wrap: anywhere; }
.mono-sm { font-family: var(--font-mono); font-size: 12px; line-height: 16px; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip { display: inline-flex; align-items: center; gap: 6px; min-height: 30px; padding: 4px 10px; border-radius: 9999px; background: var(--liner-2); font-size: 13px; line-height: 18px; }
.chip svg.i { width: 14px; height: 14px; color: var(--ink-muted); }
.field { display: flex; flex-direction: column; gap: 6px; }
.field label { font-size: 13px; line-height: 18px; font-weight: 600; }
.field .opt { font-weight: 500; color: var(--ink-muted); }
.input { width: 100%; min-height: 48px; padding: 11px 14px; border: 1px solid var(--line-strong); border-radius: var(--radius-2xl); background: var(--liner); color: var(--ink); font: inherit; box-shadow: var(--flute-e); }
.input:focus { outline: none; border-color: var(--focus); box-shadow: 0 0 0 2px var(--focus); }
textarea.input { min-height: 120px; resize: vertical; font-family: var(--font-mono); font-size: 14px; line-height: 20px; }
textarea.masked { -webkit-text-security: disc; }
.field--invalid .input { border-color: var(--danger); }
.desc { font-size: 13px; line-height: 18px; color: var(--ink-muted); }
.error { font-size: 13px; line-height: 18px; color: var(--danger); }
.notice { padding: 10px 14px; border-radius: 12px; background: var(--danger-soft); color: var(--danger); font-size: 14px; line-height: 20px; }
.pw { position: relative; }
.pw .input { padding-right: 52px; }
.eye { position: absolute; right: 6px; top: 6px; width: 36px; height: 36px; border: 0; border-radius: 12px; background: transparent; color: var(--ink-muted); display: none; place-items: center; cursor: pointer; }
.js .eye { display: grid; }
.eye:hover { background: var(--liner-2); }
.eye svg.i { width: 18px; height: 18px; }
.btn { display: inline-flex; align-items: center; justify-content: center; gap: 8px; min-height: 48px; padding: 0 20px; border: 0; border-radius: var(--radius-2xl); background: var(--kraft-strong); color: var(--on-kraft); font: inherit; font-weight: 600; cursor: pointer; box-shadow: var(--flute-c); }
.btn:active { background: var(--kraft); }
.btn--block { width: 100%; }
.btn--sm { min-height: 36px; padding: 0 14px; border-radius: 12px; font-size: 14px; background: var(--liner); color: var(--ink); border: 1px solid var(--line); box-shadow: var(--flute-e); }
.actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 4px; }
a.btn { text-decoration: none; }
.copy { display: flex; align-items: center; padding: 6px 12px; border-radius: 12px; background: var(--liner-3); max-width: 100%; }
.copy code { font-family: var(--font-mono); font-size: 13px; line-height: 20px; overflow-wrap: anywhere; }
form { margin: 0; }
@media (prefers-reduced-motion: reduce) { .btn:active { transform: none; } }
@media (max-width: 520px) { .auth { justify-content: flex-start; padding-top: 24px; } .card { padding: 20px; } }
</style>
</head>
<body>
<main class="auth">
{{if eq .State "login"}}
  <div class="col col--narrow">
    <div class="stack center">
      <span class="logo logo--lg" aria-hidden="true">{{logo}}</span>
      <h1>登录</h1>
      <span class="mono-sm muted">{{.Host}}</span>
    </div>
    <form class="stack" method="post" action="{{.Action}}" autocomplete="on">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <div class="field">
        <label for="username">用户名</label>
        <input class="input" id="username" name="username" autocomplete="username" autocapitalize="off" spellcheck="false" required>
      </div>
      <div class="field{{if .LoginError}} field--invalid{{end}}">
        <label for="password">密码</label>
        <div class="pw">
          <input class="input" id="password" name="password" type="password" autocomplete="current-password" required>
          <button class="eye" type="button" data-target="password" aria-label="显示密码">{{template "eye"}}</button>
        </div>
        {{if .LoginError}}<div class="error" role="alert">{{.LoginError}}</div>{{end}}
      </div>
      <button class="btn btn--block" type="submit">登录</button>
    </form>
  </div>
{{else}}
  <div class="col">
    <div class="spread">
      <span class="logo" aria-hidden="true">{{logo}}</span>
      {{if .Username}}<span class="who"><span class="avatar" aria-hidden="true">{{.Initial}}</span>{{.Username}}</span>{{end}}
    </div>
    <div class="card">
{{if eq .State "form"}}
      <form class="stack" method="post" action="{{.Action}}" autocomplete="off" novalidate>
        <input type="hidden" name="csrf" value="{{.CSRF}}">
        <div class="stack">
          <span class="g g--key" aria-hidden="true">{{template "key"}}</span>
          <h1>{{.Title}}</h1>
          {{if .Purpose}}<p class="small muted">{{.Purpose}}</p>{{end}}
          <div class="chips">
            {{if .Client}}<span class="chip"><svg class="i" viewBox="0 0 24 24" aria-hidden="true"><path d="M2 12c2-4 4-4 6 0s4 4 6 0 4-4 6 0"/></svg>{{.Client}}</span>{{end}}
            {{if .TaskBound}}<span class="chip"><svg class="i" viewBox="0 0 24 24" aria-hidden="true"><rect width="18" height="11" x="3" y="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>仅此任务可用</span>{{end}}
            {{if .Update}}<span class="chip"><svg class="i" viewBox="0 0 24 24" aria-hidden="true"><path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/></svg>替换当前内容</span>{{end}}
            <span class="chip"><svg class="i" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/></svg><time datetime="{{.Expires.ISO}}" data-fmt="before">{{.Expires.Text}} 前</time></span>
          </div>
        </div>
        {{if .FormError}}<div class="notice" role="alert">{{.FormError}}</div>{{end}}
        {{range .Fields}}
        <div class="field{{if .Error}} field--invalid{{end}}">
          <label for="{{.ID}}"><span class="mono">{{.Name}}</span>{{if .Label}} · {{.Label}}{{end}}{{if .Optional}} <span class="opt">可选</span>{{end}}</label>
          {{if .Multiline}}
          <div class="pw">
            <textarea class="input{{if .Secret}} masked{{end}}" id="{{.ID}}" name="{{.Name}}" autocomplete="off" autocapitalize="off" spellcheck="false"{{if not .Optional}} required{{end}}{{if .Error}} aria-invalid="true"{{end}}></textarea>
            {{if .Secret}}<button class="eye" type="button" data-target="{{.ID}}" aria-label="显示内容">{{template "eye"}}</button>{{end}}
          </div>
          {{else if .Secret}}
          <div class="pw">
            <input class="input" id="{{.ID}}" name="{{.Name}}" type="password" autocomplete="off" autocapitalize="off" spellcheck="false"{{if not .Optional}} required{{end}}{{if .Error}} aria-invalid="true"{{end}}>
            <button class="eye" type="button" data-target="{{.ID}}" aria-label="显示内容">{{template "eye"}}</button>
          </div>
          {{else}}
          <input class="input" id="{{.ID}}" name="{{.Name}}" type="text" autocomplete="off" autocapitalize="off" spellcheck="false"{{if not .Optional}} required{{end}}{{if .Error}} aria-invalid="true"{{end}}>
          {{end}}
          {{if .Error}}<div class="error" role="alert">{{.Error}}</div>{{else if .Description}}<div class="desc">{{.Description}}</div>{{end}}
        </div>
        {{end}}
        <button class="btn btn--block" type="submit">保存</button>
      </form>
{{else if eq .State "saved"}}
      <div class="stack">
        <span class="g g--ok" aria-hidden="true"><svg class="i" viewBox="0 0 24 24"><path d="M20 6 9 17l-5-5"/></svg></span>
        <h2>已保存</h2>
        <span class="copy"><code>{{.CredentialID}}</code></span>
        <p class="small muted">可以回到 Agent 了</p>
        <div class="actions">
          <a class="btn btn--sm" href="{{.SettingsURL}}">我的凭证</a>
          <a class="btn btn--sm" href="{{.ConsoleURL}}">打开控制台</a>
        </div>
      </div>
{{else if eq .State "submitted"}}
      <div class="stack">
        <span class="g g--key" aria-hidden="true">{{template "key"}}</span>
        <h2>已提交过</h2>
        <p class="small muted">在我的凭证中更新</p>
        <div class="actions">
          <a class="btn btn--sm" href="{{.SettingsURL}}">我的凭证</a>
          <a class="btn btn--sm" href="{{.ConsoleURL}}">打开控制台</a>
        </div>
      </div>
{{else if eq .State "expired"}}
      <div class="stack">
        <span class="g g--idle" aria-hidden="true"><svg class="i" viewBox="0 0 24 24"><path d="M10 2h4"/><path d="m12 14 3-3"/><circle cx="12" cy="14" r="8"/></svg></span>
        <h2>链接已过期</h2>
        <p class="small muted"><time datetime="{{.Expires.ISO}}">{{.Expires.Text}}</time> · 请 Agent 重新发起</p>
        <div class="actions">
          <a class="btn btn--sm" href="{{.ConsoleURL}}">打开控制台</a>
        </div>
      </div>
{{else if eq .State "cancelled"}}
      <div class="stack">
        <span class="g g--idle" aria-hidden="true"><svg class="i" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="m4.9 4.9 14.2 14.2"/></svg></span>
        <h2>请求已取消</h2>
        <p class="small muted">请 Agent 重新发起</p>
        <div class="actions">
          <a class="btn btn--sm" href="{{.ConsoleURL}}">打开控制台</a>
        </div>
      </div>
{{else if eq .State "other_user"}}
      <div class="stack">
        <span class="g g--idle" aria-hidden="true"><svg class="i" viewBox="0 0 24 24"><rect width="18" height="11" x="3" y="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg></span>
        <h2>不属于当前账号</h2>
        <form method="post" action="{{.SwitchAction}}">
          <input type="hidden" name="csrf" value="{{.SwitchCSRF}}">
          <button class="btn btn--sm" type="submit">切换账号</button>
        </form>
      </div>
{{else}}
      <div class="stack">
        <span class="g g--idle" aria-hidden="true"><svg class="i" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="M12 8v4"/><path d="M12 16h.01"/></svg></span>
        <h2>链接不可用</h2>
        <p class="small muted">请重新打开 Agent 给出的链接</p>
        <div class="actions">
          <a class="btn btn--sm" href="{{.ConsoleURL}}">打开控制台</a>
        </div>
      </div>
{{end}}
    </div>
  </div>
{{end}}
</main>
<script nonce="{{.Nonce}}">
(function () {
  document.documentElement.className = "js";
  var two = function (n) { return (n < 10 ? "0" : "") + n; };
  var times = document.querySelectorAll("time[datetime]");
  for (var i = 0; i < times.length; i++) {
    var d = new Date(times[i].getAttribute("datetime"));
    if (isNaN(d)) continue;
    var text = (d.getMonth() + 1) + "月" + d.getDate() + "日 " + two(d.getHours()) + ":" + two(d.getMinutes());
    var now = new Date();
    if (d.toDateString() === now.toDateString()) text = "今天 " + two(d.getHours()) + ":" + two(d.getMinutes());
    times[i].textContent = text + (times[i].getAttribute("data-fmt") === "before" ? " 前" : "");
  }
  var eyes = document.querySelectorAll(".eye");
  for (var j = 0; j < eyes.length; j++) {
    eyes[j].addEventListener("click", function () {
      var el = document.getElementById(this.getAttribute("data-target"));
      if (!el) return;
      var shown = this.getAttribute("aria-pressed") === "true";
      if (el.tagName === "TEXTAREA") el.classList.toggle("masked", shown);
      else el.type = shown ? "password" : "text";
      this.setAttribute("aria-pressed", shown ? "false" : "true");
      this.setAttribute("aria-label", shown ? "显示内容" : "隐藏内容");
    });
  }
})();
</script>
</body>
</html>
{{define "eye"}}<svg class="i" viewBox="0 0 24 24" aria-hidden="true"><path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/></svg>{{end}}
{{define "key"}}<svg class="i" viewBox="0 0 24 24"><path d="M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"/><circle cx="16.5" cy="7.5" r=".5"/></svg>{{end}}
`
