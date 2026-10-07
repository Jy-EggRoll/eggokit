/*
 * 文件类型图标（Seti）的解析器。
 *
 * 与 VSCode 用同一份图标主题文档与同一套解析顺序。之所以要运行时解析而不是编译期映射：
 * 图标主题里 fileNames/fileExtensions 只是前两级，第三级 languageIds 需要文件的语言 id，
 * 而语言 id 是 VSCode 由各语言扩展声明出来的——那张表另存在 vscode-language-map.json。
 *
 * 自定位：资源 URL 不写死，而是从本脚本自身的 URL 推出同目录下两份 JSON 的位置，
 * 因此调用方把 fileicon/ 挂在哪个路径前缀下、静态资源的根目录叫什么，都不用配置。
 *
 * 对外只暴露一个全局对象，其余状态收在闭包里，避免污染调用方的全局作用域：
 *   window.fileicon.ready            两份 JSON 就绪的 Promise，永远不会 reject
 *   window.fileicon.iconHTML(path)   取某个路径的类型图标 HTML，未就绪或查不到时给空串
 *   window.fileicon.clearCache()     清掉按文件名的查表缓存
 */
(function () {
  'use strict';

  // 本脚本在资源根下的固定路径，回退扫描 script 标签时用它做后缀匹配
  const SELF_SUFFIX = 'fileicon/seti.js';

  // selfURL 取本脚本的绝对 URL。document.currentScript 在异步注入、模块化或某些
  // 框架的加载方式下会是 null，此时退化为从后往前扫描 script[src]，找以固定路径结尾的那个
  function selfURL() {
    const cur = document.currentScript;
    if (cur && cur.src) return cur.src;
    const list = document.getElementsByTagName('script');
    for (let i = list.length - 1; i >= 0; i--) {
      const src = list[i].src || '';
      if (src.endsWith(SELF_SUFFIX)) return src;
    }
    return '';
  }

  // 去掉查询串与文件名，得到同目录前缀。取不到自身 URL 时退化为相对页面路径，
  // 这是最后的降级路径：拿不到资源只会没有图标，不该抛错
  const base = selfURL().replace(/[?#].*$/, '').replace(/\/[^/]*$/, '/');
  const THEME_URL = base + 'seti-icon-theme.json';
  const LANG_URL = base + 'vscode-language-map.json';

  let iconDark = null; // 深色主题的查找表
  let iconLight = null; // 浅色主题的查找表（Seti 的 light 段是全量平行表）
  let langMap = null; // 扩展名/文件名 -> 语言 id
  const iconCache = new Map(); // 文件名 -> 图标 id，避免同一扩展名反复查表

  const prefersLight = window.matchMedia('(prefers-color-scheme: light)');

  // 主题切换时自行清缓存：调用方只负责重画，不需要知道缓存的存在，
  // 否则深浅两套表切换后会沿用旧表查出的图标 id
  prefersLight.addEventListener('change', function () {
    clearCache();
  });

  // 自带一个最小的 HTML 转义，不依赖调用方页面里的同名工具函数
  function esc(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  // 自定位拿到的两份 JSON 都是静态资源，与页面同源；cache: 'no-store' 是为了
  // 图标主题更新后不会被浏览器缓存挡住
  function getJSON(url) {
    return fetch(url, { cache: 'no-store' }).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status + ' ' + url);
      return r.json();
    });
  }

  // ready：两份 JSON 取到后 resolve，取不到也 resolve（图标置空）。
  // 绝不 reject——图标缺席只影响观感，不该让调用方的页面渲染失败
  const ready = (async function load() {
    try {
      const [theme, langs] = await Promise.all([getJSON(THEME_URL), getJSON(LANG_URL)]);
      const defs = theme.iconDefinitions || {};
      iconDark = {
        defs,
        file: theme.file,
        fileNames: theme.fileNames || {},
        fileExtensions: theme.fileExtensions || {},
        languageIds: theme.languageIds || {},
      };
      // light 段与深色段平行，同样有 file 与三段查找表，缺哪段就退化为该段为空
      const light = theme.light || {};
      iconLight = {
        defs,
        file: light.file,
        fileNames: light.fileNames || {},
        fileExtensions: light.fileExtensions || {},
        languageIds: light.languageIds || {},
      };
      langMap = langs;
    } catch (err) {
      iconDark = null;
      iconLight = null;
      langMap = null;
      console.warn('fileicon: failed to load icon theme, file type icons are disabled: ' + err.message);
    }
  })();

  // extCandidates 按 VSCode 的规则给出某个文件名的全部候选扩展名，从最长到最短。
  // 例：foo.bar.js -> ['bar.js', 'js']；.gitignore -> ['gitignore']
  // 之所以要有多个候选：图标主题里存在 map、bash_profile 这类多点后缀
  function extCandidates(lower) {
    const out = [];
    let i = lower.indexOf('.');
    while (i !== -1) {
      const e = lower.slice(i + 1);
      if (e) out.push(e);
      i = lower.indexOf('.', i + 1);
    }
    return out;
  }

  // resolveIconId 按 VSCode 的解析顺序取图标 id：fileNames -> fileExtensions -> languageIds -> file
  function resolveIconId(base, map) {
    const lower = base.toLowerCase();
    let id = map.fileNames[lower];
    if (!id) {
      for (const e of extCandidates(lower)) {
        const hit = map.fileExtensions[e];
        if (hit) {
          id = hit;
          break;
        }
      }
    }
    if (!id && langMap) {
      let lang = langMap.byFileName[lower];
      if (!lang) {
        for (const e of extCandidates(lower)) {
          lang = langMap.byExtension[e];
          if (lang) break;
        }
      }
      if (lang) id = map.languageIds[lang];
    }
    return id || map.file;
  }

  // iconGlyph 把图标定义里的 fontCharacter 转成字符。
  // 文档里的写法是反斜杠加十六进制（形如 \E001），那是 VSCode 侧的转义表示，
  // 到了 JSON 里就是普通的反斜杠加若干位十六进制，必须按十六进制解析后再取私有区码位
  function iconGlyph(def) {
    const m = /^\\([0-9A-Fa-f]{1,6})$/.exec((def && def.fontCharacter) || '');
    return m ? String.fromCodePoint(parseInt(m[1], 16)) : '';
  }

  // baseName 取路径的最后一段。
  //
  // 同时接受 / 与 \：路径既可能来自调用方自己的常量，也可能来自服务端返回的宿主路径
  //（Windows 上会是反斜杠）
  function baseName(p) {
    return String(p || '').split(/[\\/]/).filter(Boolean).pop() || '';
  }

  // iconHTML 生成文件类型图标的 HTML。
  // 颜色用图标文档里的 fontColor（Seti 为每种类型配了色），因此图标颜色是“类型色”，
  // 与文件名、状态字母的“状态色”互不干扰——这正是 VSCode 里的观感
  function iconHTML(path) {
    const map = prefersLight.matches ? iconLight : iconDark;
    if (!map) return '';
    const name = baseName(path);
    let id = iconCache.get(name);
    if (id === undefined) {
      id = resolveIconId(name, map);
      iconCache.set(name, id);
    }
    const def = map.defs[id];
    const ch = iconGlyph(def);
    if (!ch) return '';
    const color = def.fontColor ? ' style="color:' + esc(def.fontColor) + '"' : '';
    return '<i class="seti"' + color + '>' + esc(ch) + '</i>';
  }

  function clearCache() {
    iconCache.clear();
  }

  window.fileicon = { ready: ready, iconHTML: iconHTML, clearCache: clearCache };
})();