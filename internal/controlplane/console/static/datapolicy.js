// datapolicy.js — 司域控制台中【唯一】的 JavaScript 文件。
//
// 严格的「渐进增强（progressive enhancement）」：
//   - 无 JS 基线：#cond-json textarea 默认可见，用户手填 canonical condition JSON 串，
//     表单照常 POST condition 字段。这是 canonical 提交路径，Go 测试只验这条。
//   - 有 JS：本脚本接管，露出可视化「构建器」(#builder)、隐藏 textarea，
//     提交前把构建器状态序列化成 canonical condition-tree JSON 写回 #cond-json.value。
//   - 「专业模式」按钮在构建器 ↔ 原始 textarea 之间切换，让高级用户手改原始 JSON。
//
// v2（M4.3）：
//   - 递归嵌套盒 AND/OR/NOT（NOT 恰 1 子项），组内可加叶子/子组。
//   - 13 个 canonical【大写】叶子算子；value 按算子自适应（无/单框/数组框/两框）。
//   - field 实时白名单校验 ^[A-Za-z_]\w*$，非法行内标红。
//   - 序列化产出【大写】算子（修复历史小写/contains/单层 bug——数据面引擎只认大写文法）。
//   - 构建器变动防抖 ~300ms → fetch 本域预览端点 → textContent 内联展示谓词/错误。
//
// 纯 vanilla，零网络库；fetch 只打同源预览端点。condition 永远以 canonical JSON 串提交，
// 由后端 fail-close 校验（与数据面 eval 同一文法定义）。
(function () {
  "use strict";

  // canonical【大写】算子表——序列化与下拉均以此为准（修文法 bug 的核心）。
  var LOGICAL_OPS = ["AND", "OR", "NOT"];
  var LEAF_OPS = ["EQ", "NE", "GT", "GE", "LT", "LE", "IN", "NOT_IN", "LIKE", "NOT_LIKE", "IS_NULL", "IS_NOT_NULL", "BETWEEN"];
  var FIELD_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
  var PREVIEW_DEBOUNCE_MS = 300; // 构建器变动 → 预览端点的防抖窗口

  document.addEventListener("DOMContentLoaded", function () {
    var form = document.getElementById("dp-form");
    var textarea = document.getElementById("cond-json");
    var builder = document.getElementById("builder");
    var toggle = document.getElementById("builder-toggle");
    var preview = document.getElementById("cond-preview");
    if (!form || !textarea || !builder || !toggle) {
      return; // 防御：缺元素则保持无 JS 基线（textarea 可见可提交）
    }

    // rawMode=true 展示原始 textarea（专业模式）；false 展示可视化构建器。
    var rawMode = false;
    var rootGroupEl = null; // 构建器根组元素（始终是一个组）
    var previewTimer = null;

    // ————— 值解析（构建器输入 → canonical JSON 值）—————

    // parseScalar：单框文本 → 标量值（保真优先，绝不静默腐化权限条件值）。先 trim；空串→字符串；
    // $user.xxx 变量原样（已 trim，前后空白鲁棒）；true/false→布尔（保真 round-trip，不腐化成字符串）；
    // 仅【严格且可逆】的数字才转 Number（雪花 ID / hex / 前导 0 / 科学计数一律留原字符串，防静默改值）；
    // 其余保留原字符串。
    function parseScalar(raw) {
      var t = String(raw === undefined || raw === null ? "" : raw).trim();
      if (t === "") return ""; // 空串保持字符串，绝不强转成 0
      if (t.charAt(0) === "$") return t; // $user.xxx 变量原样
      if (t === "true") return true; // 布尔保真（关键1/次要2）
      if (t === "false") return false;
      if (/^-?\d+(\.\d+)?$/.test(t) && String(Number(t)) === t) return Number(t); // 仅严格可逆才转数字
      return t; // 其余（含大整数/hex/前导0/科学计数）保留原字符串
    }

    // parseArray：逗号分隔文本 → 数组（trim、丢空 token，每 token 走 parseScalar）。
    function parseArray(text) {
      var out = [];
      var parts = String(text === undefined || text === null ? "" : text).split(",");
      for (var i = 0; i < parts.length; i++) {
        var t = parts[i].trim();
        if (t === "") continue;
        out.push(parseScalar(t));
      }
      return out;
    }

    // scalarToText：已有 JSON 值 → 输入框回显文本（hydrate 用）。
    function scalarToText(v) {
      if (v === null || v === undefined) return "";
      return String(v);
    }

    // ————— 节点类型判定（hydrate 用）—————

    function isGroupNode(n) {
      return n && typeof n === "object" && LOGICAL_OPS.indexOf(n.op) >= 0;
    }
    function isLeafNode(n) {
      return n && typeof n === "object" && LEAF_OPS.indexOf(n.op) >= 0 && typeof n.field === "string";
    }

    // directChildNodes：取容器里的直接叶子/组子节点（跳过 head/actions 等结构 div）。
    function directChildNodes(container) {
      var out = [];
      for (var i = 0; i < container.children.length; i++) {
        var c = container.children[i];
        if (c.dataset && (c.dataset.node === "group" || c.dataset.node === "leaf")) out.push(c);
      }
      return out;
    }

    // ————— DOM 搭建 —————

    // mkValueInput：造一个带 aria-label 的值输入框，input 事件触发防抖预览。
    function mkValueInput(cls, placeholder, ariaLabel) {
      var inp = document.createElement("input");
      inp.type = "text";
      inp.className = cls;
      inp.placeholder = placeholder;
      inp.setAttribute("aria-label", ariaLabel);
      inp.addEventListener("input", scheduleUpdate);
      return inp;
    }

    // valueShape：算子决定的 value 输入形状；切换算子时形状不变则保留已填值（次要4）。
    function valueShape(op) {
      if (op === "IS_NULL" || op === "IS_NOT_NULL") return "none";
      if (op === "IN" || op === "NOT_IN") return "array";
      if (op === "BETWEEN") return "between";
      return "scalar";
    }

    // buildValue：按算子自适应重建叶子的 value 区（无/单框/数组框/两框），可选 preset 回填。
    function buildValue(valWrap, op, preset) {
      valWrap.textContent = ""; // 清空重建
      if (op === "IS_NULL" || op === "IS_NOT_NULL") {
        return; // 无 value 输入
      }
      if (op === "IN" || op === "NOT_IN") {
        var arrIn = mkValueInput("cond-val-array", "逗号分隔，如 pending, approved", "值列表（逗号分隔）");
        if (Array.isArray(preset)) arrIn.value = preset.map(scalarToText).join(", ");
        valWrap.appendChild(arrIn);
        return;
      }
      if (op === "BETWEEN") {
        var lo = mkValueInput("cond-val-lo", "下界", "区间下界");
        var hi = mkValueInput("cond-val-hi", "上界", "区间上界");
        if (Array.isArray(preset)) {
          if (preset.length > 0) lo.value = scalarToText(preset[0]);
          if (preset.length > 1) hi.value = scalarToText(preset[1]);
        }
        valWrap.appendChild(lo);
        valWrap.appendChild(hi);
        return;
      }
      // 标量比较 / LIKE / NOT_LIKE：单框。
      var one = mkValueInput("cond-val", "value，如 $user.dept", "值");
      if (preset !== undefined && preset !== null && !Array.isArray(preset)) one.value = scalarToText(preset);
      valWrap.appendChild(one);
    }

    // validateField：实时 field 白名单校验，非法（非空且不匹配）标红。
    function validateField(fieldIn) {
      var v = fieldIn.value.trim();
      var invalid = v !== "" && !FIELD_RE.test(v);
      fieldIn.classList.toggle("cond-field-invalid", invalid);
      if (invalid) fieldIn.setAttribute("aria-invalid", "true");
      else fieldIn.removeAttribute("aria-invalid");
    }

    // buildLeafRow：叶子行 = field 框 + op select（13 大写）+ 自适应 value 区 + 删除。
    function buildLeafRow(node) {
      node = node || {};
      var row = document.createElement("div");
      row.className = "cond-leaf";
      row.dataset.node = "leaf";

      var fieldIn = document.createElement("input");
      fieldIn.type = "text";
      fieldIn.className = "cond-field";
      fieldIn.placeholder = "字段，如 dept";
      fieldIn.setAttribute("aria-label", "字段名");
      if (typeof node.field === "string") fieldIn.value = node.field;
      fieldIn.addEventListener("input", function () {
        validateField(fieldIn);
        scheduleUpdate();
      });

      var opSel = document.createElement("select");
      opSel.className = "cond-op";
      opSel.setAttribute("aria-label", "运算符");
      LEAF_OPS.forEach(function (op) {
        var o = document.createElement("option");
        o.value = op;
        o.textContent = op;
        opSel.appendChild(o);
      });
      var op = LEAF_OPS.indexOf(node.op) >= 0 ? node.op : "EQ";
      opSel.value = op;
      var prevOp = op;

      var valWrap = document.createElement("span");
      valWrap.className = "cond-value";

      opSel.addEventListener("change", function () {
        var newOp = opSel.value;
        // 形状变了才重建（清空）；同属单框标量 / 数组形状互切则保留已填值（次要4）。
        if (valueShape(newOp) !== valueShape(prevOp)) {
          buildValue(valWrap, newOp, undefined);
        }
        prevOp = newOp;
        scheduleUpdate();
      });

      var del = document.createElement("button");
      del.type = "button";
      del.className = "cond-del btn-sm";
      del.textContent = "×";
      del.setAttribute("aria-label", "删除该条件");
      del.addEventListener("click", function () {
        var kids = row.parentNode;
        kids.removeChild(row);
        var pg = kids.parentNode;
        if (pg && pg.dataset && pg.dataset.node === "group") afterChildChange(pg);
        scheduleUpdate();
      });

      row.appendChild(fieldIn);
      row.appendChild(opSel);
      row.appendChild(valWrap);
      row.appendChild(del);

      validateField(fieldIn);
      buildValue(valWrap, op, node.value);
      return row;
    }

    // buildGroup：递归渲染组盒 = 组合 select（AND/OR/NOT）+ 子项容器 + 「+条件」「+子组」+ 删除（非根）。
    function buildGroup(node, isRoot) {
      node = node || {};
      var group = document.createElement("div");
      group.className = "cond-group";
      group.dataset.node = "group";

      var head = document.createElement("div");
      head.className = "cond-group-head";

      var comboSel = document.createElement("select");
      comboSel.className = "cond-combinator";
      comboSel.setAttribute("aria-label", "条件组合方式（AND/OR/NOT）");
      LOGICAL_OPS.forEach(function (op) {
        var o = document.createElement("option");
        o.value = op;
        o.textContent = op;
        comboSel.appendChild(o);
      });
      comboSel.value = LOGICAL_OPS.indexOf(node.op) >= 0 ? node.op : "AND";
      comboSel.addEventListener("change", function () {
        afterChildChange(group); // 更新 NOT 样式 + 加满/超额时门控「+」按钮
        scheduleUpdate();
      });
      head.appendChild(comboSel);

      if (!isRoot) {
        var delGroup = document.createElement("button");
        delGroup.type = "button";
        delGroup.className = "cond-del btn-sm";
        delGroup.textContent = "×";
        delGroup.setAttribute("aria-label", "删除该条件组");
        delGroup.addEventListener("click", function () {
          var kids = group.parentNode;
          kids.removeChild(group);
          var pg = kids.parentNode;
          if (pg && pg.dataset && pg.dataset.node === "group") afterChildChange(pg);
          scheduleUpdate();
        });
        head.appendChild(delGroup);
      }
      group.appendChild(head);

      var kids = document.createElement("div");
      kids.className = "cond-group-children";
      group.appendChild(kids);

      var actions = document.createElement("div");
      actions.className = "cond-group-actions";
      var addLeaf = document.createElement("button");
      addLeaf.type = "button";
      addLeaf.className = "cond-add btn-sm";
      addLeaf.textContent = "+ 条件";
      addLeaf.setAttribute("aria-label", "在该组添加条件");
      addLeaf.addEventListener("click", function () {
        kids.appendChild(buildLeafRow({}));
        afterChildChange(group);
        scheduleUpdate();
      });
      var addGroup = document.createElement("button");
      addGroup.type = "button";
      addGroup.className = "cond-add btn-sm";
      addGroup.textContent = "+ 子组";
      addGroup.setAttribute("aria-label", "在该组添加子组");
      addGroup.addEventListener("click", function () {
        kids.appendChild(buildGroup({ op: "AND", children: [] }, false));
        afterChildChange(group);
        scheduleUpdate();
      });
      actions.appendChild(addLeaf);
      actions.appendChild(addGroup);
      group.appendChild(actions);

      // hydrate 子项。
      var children = Array.isArray(node.children) ? node.children : [];
      children.forEach(function (ch) {
        if (isGroupNode(ch)) kids.appendChild(buildGroup(ch, false));
        else if (isLeafNode(ch)) kids.appendChild(buildLeafRow(ch));
      });

      afterChildChange(group); // 初始化 NOT 样式 + 按钮门控（并把非法的 NOT 多子项裁到 1）
      return group;
    }

    // afterChildChange：维持 NOT 恰 1 子项不变量——NOT 时裁掉多余子项、加满即隐藏「+」按钮，
    // 并切换 NOT 视觉样式。任何增删子项 / 切换组合算子后调用。
    function afterChildChange(group) {
      var comboSel = group.querySelector(":scope > .cond-group-head > .cond-combinator");
      var kids = group.querySelector(":scope > .cond-group-children");
      var actions = group.querySelector(":scope > .cond-group-actions");
      var isNot = comboSel.value === "NOT";
      group.classList.toggle("cond-group--not", isNot);
      if (isNot) {
        var nodes = directChildNodes(kids);
        for (var i = nodes.length - 1; i >= 1; i--) kids.removeChild(nodes[i]); // 只留第 1 个
      }
      var count = directChildNodes(kids).length;
      actions.style.display = isNot && count >= 1 ? "none" : ""; // NOT 加满 → 隐藏「+」
    }

    // ————— 序列化（构建器 → canonical【大写】JSON）—————

    function serializeNode(el) {
      if (el.dataset.node === "group") return serializeGroup(el);
      if (el.dataset.node === "leaf") return serializeLeaf(el);
      return null;
    }

    // serializeGroup：组 → {op:<大写>, children:[非空子项...]}；跳过空子项；
    // AND/OR 空 → null（跳过）；NOT → 恰取第 1 子项，空 → null。
    function serializeGroup(group) {
      var op = group.querySelector(":scope > .cond-group-head > .cond-combinator").value;
      var kids = group.querySelector(":scope > .cond-group-children");
      var nodes = directChildNodes(kids);
      var children = [];
      for (var i = 0; i < nodes.length; i++) {
        var c = serializeNode(nodes[i]);
        if (c !== null) children.push(c);
      }
      if (op === "NOT") {
        if (children.length === 0) return null;
        return { op: "NOT", children: [children[0]] };
      }
      if (children.length === 0) return null; // 空 AND/OR 组不产出非法 JSON
      return { op: op, children: children };
    }

    // serializeLeaf：叶子 → {field, op:<大写>, value:<按 op 成形>}；空 field → null（跳过空行）。
    // IS_NULL/IS_NOT_NULL 无 value；IN/NOT_IN → 数组；BETWEEN → 2 元数组；其余 → 标量。
    function serializeLeaf(leaf) {
      var field = leaf.querySelector(":scope > .cond-field").value.trim();
      if (field === "") return null;
      var op = leaf.querySelector(":scope > .cond-op").value;
      var valWrap = leaf.querySelector(":scope > .cond-value");
      if (op === "IS_NULL" || op === "IS_NOT_NULL") {
        return { field: field, op: op }; // 无 value
      }
      if (op === "IN" || op === "NOT_IN") {
        var arrIn = valWrap.querySelector(".cond-val-array");
        return { field: field, op: op, value: parseArray(arrIn ? arrIn.value : "") };
      }
      if (op === "BETWEEN") {
        var lo = valWrap.querySelector(".cond-val-lo");
        var hi = valWrap.querySelector(".cond-val-hi");
        return { field: field, op: op, value: [parseScalar(lo ? lo.value : ""), parseScalar(hi ? hi.value : "")] };
      }
      var one = valWrap.querySelector(".cond-val");
      return { field: field, op: op, value: parseScalar(one ? one.value : "") };
    }

    // serializeRoot：根组始终序列化——即便为空也产出 {op:<根组合>, children:[]}（交服务端 fail-close）。
    function serializeRoot() {
      var node = rootGroupEl ? serializeNode(rootGroupEl) : null;
      if (node === null) {
        var comboSel = rootGroupEl && rootGroupEl.querySelector(":scope > .cond-group-head > .cond-combinator");
        node = { op: comboSel ? comboSel.value : "AND", children: [] };
      }
      return JSON.stringify(node);
    }

    // ————— hydrate（原始 JSON → 嵌套构建器）—————

    // parseRootFromRaw：解析 textarea 里的 canonical JSON 为根节点对象；不可解析 → null。
    // 裸叶子根（如 {"field":..,"op":"BETWEEN",..}）包一层 AND 组（构建器根恒为组）。
    function parseRootFromRaw(raw) {
      raw = (raw || "").trim();
      if (raw === "") return null;
      var tree;
      try {
        tree = JSON.parse(raw);
      } catch (e) {
        return null;
      }
      if (!tree || typeof tree !== "object") return null;
      if (isGroupNode(tree)) return tree;
      if (isLeafNode(tree)) return { op: "AND", children: [tree] };
      return null;
    }

    // mountRoot：清空构建器并挂一棵新根组（本脚本完全拥有 #builder 内容）。
    function mountRoot(rootNode) {
      builder.textContent = "";
      rootGroupEl = buildGroup(rootNode || { op: "AND", children: [] }, true);
      builder.appendChild(rootGroupEl);
    }

    // ————— 预览（防抖 → 本域预览端点）—————

    function scheduleUpdate() {
      if (rawMode) return;
      if (previewTimer) clearTimeout(previewTimer);
      previewTimer = setTimeout(runPreview, PREVIEW_DEBOUNCE_MS);
    }

    function previewURL() {
      return form.action.replace(/\/+$/, "") + "/preview-condition";
    }

    function runPreview() {
      if (rawMode || !preview) return;
      var node = rootGroupEl ? serializeNode(rootGroupEl) : null;
      if (node === null) {
        preview.textContent = ""; // 尚无有效条件，不打端点
        preview.classList.remove("cond-preview--error");
        return;
      }
      var csrfEl = form.querySelector("[name=csrf_token]");
      var csrf = csrfEl ? csrfEl.value : "";
      var body = "csrf_token=" + encodeURIComponent(csrf) + "&condition=" + encodeURIComponent(JSON.stringify(node));
      fetch(previewURL(), {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        credentials: "same-origin",
        body: body
      })
        .then(function (resp) {
          return resp.json();
        })
        .then(function (data) {
          if (!preview) return;
          if (data && data.error) {
            preview.textContent = data.error; // textContent 防 XSS
            preview.classList.add("cond-preview--error");
          } else {
            preview.textContent = data && data.predicate ? data.predicate : "";
            preview.classList.remove("cond-preview--error");
          }
        })
        .catch(function () {
          if (preview) {
            preview.textContent = "";
            preview.classList.remove("cond-preview--error");
          }
        });
    }

    // ————— 模式切换 —————

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
        scheduleUpdate(); // 回到构建器时刷新一次预览
      }
    }

    toggle.addEventListener("click", function () {
      if (!rawMode) {
        // 切到专业模式前：把当前序列化落进 textarea，供手改。
        textarea.value = serializeRoot();
        setMode(true);
      } else {
        // 切回构建器：尝试用（可能手改过的）textarea 重建构建器；不可解析则留在专业模式提示修正。
        var r = parseRootFromRaw(textarea.value);
        if (!r) {
          if (preview) {
            preview.textContent = "无法解析的 JSON，请修正后再切换到可视化模式";
            preview.classList.add("cond-preview--error");
          }
          return;
        }
        mountRoot(r);
        setMode(false);
      }
    });

    // 提交前：非专业模式则用构建器序列化覆盖 condition；专业模式保留用户手改的原始 JSON。
    form.addEventListener("submit", function () {
      if (!rawMode) {
        textarea.value = serializeRoot();
      }
    });

    // ————— 初始化 —————

    var initialRoot = parseRootFromRaw(textarea.value);
    var startRaw = false;
    if (initialRoot) {
      mountRoot(initialRoot); // 预填已有 canonical 条件
    } else if (textarea.value.trim() !== "") {
      // 非空但不可解析：保留原始内容，起始进专业模式（不隐藏用户数据），构建器备好空根。
      mountRoot({ op: "AND", children: [] });
      startRaw = true;
    } else {
      // 空白新表单：根组 + 一行空叶子，便于上手。
      mountRoot({ op: "AND", children: [] });
      var rootKids = rootGroupEl.querySelector(":scope > .cond-group-children");
      rootKids.appendChild(buildLeafRow({}));
      afterChildChange(rootGroupEl);
    }
    toggle.style.display = ""; // JS 在跑，露出切换按钮
    setMode(startRaw);
  });
})();
