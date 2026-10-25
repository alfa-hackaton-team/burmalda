package userapi

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
)

func LoginPage(enabled bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !enabled {
			http.NotFound(w, r)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			methodNotAllowed("GET, HEAD")(w, r)
			return
		}
		nonce := make([]byte, 32)
		if _, e := rand.Read(nonce); e != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		token := base64.RawStdEncoding.EncodeToString(nonce)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+token+"'; style-src 'nonce-"+token+"'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		if r.Method == "HEAD" {
			return
		}
		fmt.Fprintf(w, `<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Вход в сервис аналитики</title>
<style nonce="%s">body{font:16px system-ui;background:#f5f6f8;color:#161922;max-width:420px;margin:12vh auto;padding:24px}form{background:white;padding:28px;border-radius:16px}label{display:block;margin:18px 0}input,button{box-sizing:border-box;width:100%%;padding:12px;margin-top:8px;font:inherit;border:1px solid #ccc;border-radius:8px}button{background:#df1729;color:white;border:0;cursor:pointer}#status{white-space:pre-wrap}</style>
<form id="login"><h1>Вход в аналитику</h1><p>Используйте учётную запись, созданную администратором.</p><label>Логин<input name="login" autocomplete="username" required maxlength="200"></label><label>Пароль<input name="password" type="password" autocomplete="current-password" required maxlength="1024"></label><button>Войти</button><p id="status" role="status"></p></form>
<script nonce="%s">document.getElementById('login').addEventListener('submit',async e=>{e.preventDefault();const form=e.target,button=form.querySelector('button'),status=document.getElementById('status');button.disabled=true;status.textContent='Вход…';try{const res=await fetch('/v0.1/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'same-origin',body:JSON.stringify({login:form.elements.login.value,password:form.elements.password.value})});const body=await res.json();if(!res.ok)throw Error(body.message||'Не удалось войти');form.elements.password.value='';status.textContent='Вход выполнен. Вернитесь в приложение и обновите страницу.'}catch(err){status.textContent=err.message||'Сервис недоступен'}finally{button.disabled=false}});</script></html>`, token, token)
	})
}
