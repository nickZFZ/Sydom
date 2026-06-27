// datapolicy.js — 司域控制台中【唯一】的 JavaScript 文件。
//
// 严格的「渐进增强（progressive enhancement）」：
//   - 无 JS 基线：#cond-json textarea 默认可见，用户手填原始 condition JSON 串，
//     表单照常 POST condition 字段。这是 canonical 提交路径，Go 测试只验这条。
//   - 有 JS：本脚本接管，露出可视化「构建器」(#builder)、隐藏 textarea，
//     提交前把构建器状态序列化成合法 condition-tree JSON 写回 #cond-json.value。
//   - 「专业模式」按钮在构建器 ↔ 原始 textarea 之间切换，让高级用户手改原始 JSON。
//
// 纯 vanilla，零网络请求。condition 永远以原始 JSON 串提交，由后端 fail-close 校验。
(function () {
  "use strict";

  document.addEventListener("DOMContentLoaded", function () {
    var form = document.getElementById("dp-form");
    var textarea = document.getElementById("cond-json");
    var builder = document.getElementById("builder");
    var toggle = document.getElementById("builder-toggle");
    if (!form || !textarea || !builder) {
      return; // 防御：缺元素则保持无 JS 基线
    }

    // rawMode=true 表示当前展示原始 textarea；false 表示展示可视化构建器。
    var rawMode = false;

    // —— 构建器 DOM 搭建 ——
    // 顶部 AND/OR 组选择器 + 行容器 + 「添加条件」按钮。
    var groupRow = document.createElement("div");
    groupRow.className = "builder-group";
    var groupLabel = document.createElement("span");
    groupLabel.textContent = "组合：";
    var groupSel = document.createElement("select");
    groupSel.id = "builder-op";
    groupSel.setAttribute("aria-label", "条件组合方式（AND/OR）"); // a11y：可访问名（M3.4b axe select-name）
    ["and", "or"].forEach(function (op) {
      var o = document.createElement("option");
      o.value = op;
      o.textContent = op.toUpperCase();
      groupSel.appendChild(o);
    });
    groupRow.appendChild(groupLabel);
    groupRow.appendChild(groupSel);
    builder.appendChild(groupRow);

    var rows = document.createElement("div");
    rows.id = "builder-rows";
    builder.appendChild(rows);

    var addBtn = document.createElement("button");
    addBtn.type = "button";
    addBtn.textContent = "+ 添加条件";
    addBtn.addEventListener("click", function () {
      addRow();
    });
    builder.appendChild(addBtn);

    var OPS = ["eq", "ne", "gt", "lt", "in", "contains"];

    // addRow 追加一行 field / op / value + 删除按钮。
    function addRow(field, op, value) {
      var row = document.createElement("div");
      row.className = "builder-row";

      var fieldIn = document.createElement("input");
      fieldIn.className = "bf-field";
      fieldIn.placeholder = "field，如 dept";
      if (field) fieldIn.value = field;

      var opSel = document.createElement("select");
      opSel.className = "bf-op";
      opSel.setAttribute("aria-label", "条件运算符"); // a11y：可访问名（M3.4b axe select-name）
      OPS.forEach(function (o) {
        var opt = document.createElement("option");
        opt.value = o;
        opt.textContent = o;
        if (o === op) opt.selected = true;
        opSel.appendChild(opt);
      });

      var valIn = document.createElement("input");
      valIn.className = "bf-value";
      valIn.placeholder = "value，如 $user.dept";
      if (value) valIn.value = value;

      var del = document.createElement("button");
      del.type = "button";
      del.className = "danger";
      del.textContent = "×";
      del.addEventListener("click", function () {
        rows.removeChild(row);
      });

      row.appendChild(fieldIn);
      row.appendChild(opSel);
      row.appendChild(valIn);
      row.appendChild(del);
      rows.appendChild(row);
    }

    // serialize 读取构建器状态 → 合法 condition-tree JSON 串。
    function serialize() {
      var children = [];
      var rowEls = rows.querySelectorAll(".builder-row");
      for (var i = 0; i < rowEls.length; i++) {
        var el = rowEls[i];
        var field = el.querySelector(".bf-field").value.trim();
        var op = el.querySelector(".bf-op").value;
        var value = el.querySelector(".bf-value").value;
        if (field === "") continue; // 跳过空行
        children.push({ field: field, op: op, value: value });
      }
      return JSON.stringify({ op: groupSel.value, children: children });
    }

    // 从已存在的 textarea 值预填构建器（若是本脚本可解析的形状）。
    function hydrateFromTextarea() {
      var raw = textarea.value.trim();
      if (raw === "") return;
      try {
        var tree = JSON.parse(raw);
        if (tree && (tree.op === "and" || tree.op === "or")) {
          groupSel.value = tree.op;
          var kids = Array.isArray(tree.children) ? tree.children : [];
          kids.forEach(function (k) {
            if (k && typeof k === "object") {
              addRow(k.field, k.op, k.value);
            }
          });
        }
      } catch (e) {
        // 不可解析：保持空构建器，用户可用「专业模式」回到原始 textarea。
      }
    }

    // setMode 切换可见性（builder ↔ raw textarea）。
    function setMode(raw) {
      rawMode = raw;
      if (raw) {
        builder.style.display = "none";
        textarea.style.display = "";
        toggle.textContent = "可视化模式";
      } else {
        builder.style.display = "";
        textarea.style.display = "none";
        toggle.textContent = "专业模式（原始 JSON）";
      }
    }

    // —— 初始化 ——
    hydrateFromTextarea();
    if (rows.querySelectorAll(".builder-row").length === 0) {
      addRow(); // 至少给一行空模板，便于上手
    }
    toggle.style.display = ""; // JS 在跑，露出切换按钮
    setMode(false); // 默认进可视化构建器（textarea 隐藏，仍是表单字段）

    toggle.addEventListener("click", function () {
      if (!rawMode) {
        // 切到原始模式前，先把当前构建器状态落进 textarea，方便手改。
        textarea.value = serialize();
      }
      setMode(!rawMode);
    });

    // 提交前：仅当处于可视化模式时，用构建器状态覆盖 condition；
    // 原始模式则保留 textarea 现值（用户手改的原始 JSON）。
    form.addEventListener("submit", function () {
      if (!rawMode) {
        textarea.value = serialize();
      }
    });
  });
})();
