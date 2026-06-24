// interactions.js —— 司域控制台 M3.4a 渐进增强（toast 自消失 + 破坏性表单 dialog 二次确认）。
// 无此脚本时页面功能完整：静态 flash 条仍显示、破坏性表单 POST 落到服务端确认页 ops_confirm.html。
// 纯 vanilla，零网络请求。
(function () {
  "use strict";

  // ① toast：4s 后淡出 + × 可手动关闭。
  function initToasts() {
    document.querySelectorAll("[data-toast]").forEach(function (el) {
      var close = el.querySelector(".toast-close");
      if (close) close.addEventListener("click", function () { el.remove(); });
      setTimeout(function () { el.classList.add("toast-hide"); }, 4000);
      el.addEventListener("transitionend", function () {
        if (el.classList.contains("toast-hide")) el.remove();
      });
    });
  }

  // ② 破坏性表单：拦截提交 → <dialog> 模态确认 → 确认即追加隐藏 confirmed=1 提交到原 action。
  function initConfirms() {
    var forms = document.querySelectorAll("form[data-confirm]");
    if (!forms.length || typeof HTMLDialogElement === "undefined") return; // 不支持 dialog → 退化为服务端确认页
    forms.forEach(function (form) {
      form.addEventListener("submit", function (e) {
        if (form.dataset.confirmed === "1") return; // 已确认，放行
        e.preventDefault();
        showConfirm(form.getAttribute("data-confirm"), function () {
          var hidden = document.createElement("input");
          hidden.type = "hidden"; hidden.name = "confirmed"; hidden.value = "1";
          form.appendChild(hidden);
          form.dataset.confirmed = "1";
          form.submit();
        });
      });
    });
  }

  function showConfirm(message, onOk) {
    var dlg = document.createElement("dialog");
    dlg.className = "dialog";
    dlg.setAttribute("role", "alertdialog");
    var p = document.createElement("p");
    p.id = "confirm-msg"; p.textContent = message; dlg.appendChild(p);
    dlg.setAttribute("aria-labelledby", "confirm-msg");
    var actions = document.createElement("div");
    actions.className = "dialog-actions";
    var ok = document.createElement("button"); ok.type = "button"; ok.className = "btn danger"; ok.textContent = "确认";
    var cancel = document.createElement("button"); cancel.type = "button"; cancel.className = "btn"; cancel.textContent = "取消";
    actions.appendChild(ok); actions.appendChild(cancel); dlg.appendChild(actions);
    document.body.appendChild(dlg);
    var trigger = document.activeElement;
    cancel.addEventListener("click", function () { dlg.close(); });
    ok.addEventListener("click", function () { dlg.close(); onOk(); });
    dlg.addEventListener("close", function () { dlg.remove(); if (trigger && trigger.focus) trigger.focus(); });
    dlg.showModal(); // 原生 showModal 自带焦点陷阱 + ESC 关闭
    ok.focus();
  }

  document.addEventListener("DOMContentLoaded", function () { initToasts(); initConfirms(); });
})();
