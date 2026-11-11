//go:build js && wasm

package main

import "syscall/js"

// hideBoot 隐藏 index.html 里的加载遮罩。只在第一帧之后调一次。
func hideBoot() {
	fn := js.Global().Get("__mygoBoot")
	if fn.IsUndefined() || fn.IsNull() {
		return
	}
	if h := fn.Get("hide"); h.Type() == js.TypeFunction {
		h.Invoke()
	}
}

// pageOrigin 返回页面自身的来源（如 http://<your-server-ip>:8090）。
// 部署时由同一台机器反代 /api，所以默认就跟页面同源。
func pageOrigin() string {
	loc := js.Global().Get("location")
	if loc.IsUndefined() || loc.IsNull() {
		return ""
	}
	o := loc.Get("origin").String()
	if o == "null" || o == "" {
		return ""
	}
	return o
}

// loadStr / saveStr 用 localStorage 持久化设置。
func loadStr(key string) string {
	ls := js.Global().Get("localStorage")
	if ls.IsUndefined() || ls.IsNull() {
		return ""
	}
	v := ls.Call("getItem", key)
	if v.IsNull() || v.IsUndefined() {
		return ""
	}
	return v.String()
}

func saveStr(key, val string) {
	ls := js.Global().Get("localStorage")
	if ls.IsUndefined() || ls.IsNull() {
		return
	}
	ls.Call("setItem", key, val)
}
