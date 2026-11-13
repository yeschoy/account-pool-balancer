// essevin capture: page-side collector.
// Injected into every HTML document. Records the fingerprint surface the page
// can see, and records what site scripts touch of that surface. The beacon goes
// to https://essevin.capture/fp which only resolves inside the capture proxy.
(function () {
  if (window.__EV_FP__) return;
  window.__EV_FP__ = true;

  var HOOK = 'https://essevin.capture/fp';
  var req = [];
  window.__EV_REQ__ = req;

  function log(kind, name) { try { req.push({ kind: kind, name: name, t: Date.now() }); } catch (e) {} }

  ['userAgent', 'platform', 'language', 'languages', 'hardwareConcurrency',
   'deviceMemory', 'webdriver', 'vendor', 'maxTouchPoints'].forEach(function (f) {
    try {
      var d = Object.getOwnPropertyDescriptor(Navigator.prototype, f);
      if (!d || !d.get) return;
      Object.defineProperty(Object.getPrototypeOf(navigator), f, {
        configurable: true,
        get: function () { log('navigator.get', f); return d.get.call(this); }
      });
    } catch (e) {}
  });

  try {
    var toDataURL = HTMLCanvasElement.prototype.toDataURL;
    HTMLCanvasElement.prototype.toDataURL = function () {
      log('canvas', 'toDataURL'); return toDataURL.apply(this, arguments);
    };
    var gid = CanvasRenderingContext2D.prototype.getImageData;
    CanvasRenderingContext2D.prototype.getImageData = function () {
      log('canvas', 'getImageData'); return gid.apply(this, arguments);
    };
  } catch (e) {}

  try {
    var gp = WebGLRenderingContext.prototype.getParameter;
    WebGLRenderingContext.prototype.getParameter = function () {
      log('webgl', 'getParameter'); return gp.apply(this, arguments);
    };
  } catch (e) {}

  try {
    if (window.RTCPeerConnection && !window.RTCPeerConnection.__wrapped) {
      var R0 = window.RTCPeerConnection;
      var W = function (c) { log('webrtc', 'RTCPeerConnection'); return new R0(c); };
      W.__wrapped = 1;
      window.RTCPeerConnection = W;
    }
    if (navigator.mediaDevices) {
      ['enumerateDevices', 'getUserMedia'].forEach(function (m) {
        var f = navigator.mediaDevices[m];
        if (f && !f.__wrapped) {
          navigator.mediaDevices[m] = function () { log('webrtc', m); return f.apply(this, arguments); };
          navigator.mediaDevices[m].__wrapped = 1;
        }
      });
    }
  } catch (e) {}

  try {
    if (window.fetch && !window.fetch.__wrapped) {
      var f0 = window.fetch;
      window.fetch = function (u) {
        log('fetch', String(typeof u === 'string' ? u : (u && u.url)));
        return f0.apply(this, arguments);
      };
      window.fetch.__wrapped = 1;
    }
  } catch (e) {}

  try {
    var x0 = XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.open = function (m, u) {
      log('xhr', m + ' ' + u); return x0.apply(this, arguments);
    };
    var s0 = XMLHttpRequest.prototype.send;
    XMLHttpRequest.prototype.send = function (b) {
      try { log('xhr.body', String(b).slice(0, 400)); } catch (e) {}
      return s0.apply(this, arguments);
    };
  } catch (e) {}

  function snapshot() {
    var obs = { t: Date.now(), url: location.href };
    try {
      var n = navigator, s = screen;
      obs.navigator = {
        userAgent: n.userAgent,
        appVersion: n.appVersion,
        platform: n.platform,
        vendor: n.vendor,
        productSub: n.productSub,
        language: n.language,
        languages: (n.languages || []).slice(),
        hardwareConcurrency: n.hardwareConcurrency,
        deviceMemory: n.deviceMemory,
        maxTouchPoints: n.maxTouchPoints,
        webdriver: n.webdriver,
        pdfViewerEnabled: n.pdfViewerEnabled,
        doNotTrack: n.doNotTrack,
        cookieEnabled: n.cookieEnabled,
        plugins: Array.prototype.map.call(n.plugins || [], function (p) { return p.name; }),
        mimeTypesCount: (n.mimeTypes || []).length
      };
      obs.screen = {
        w: s.width, h: s.height, aw: s.availWidth, ah: s.availHeight,
        cd: s.colorDepth, pd: s.pixelDepth,
        dpr: window.devicePixelRatio, innerW: innerWidth, innerH: innerHeight
      };
      obs.timezone = {
        tz: Intl.DateTimeFormat().resolvedOptions().timeZone,
        offset: new Date().getTimezoneOffset()
      };
      obs.locale = Intl.DateTimeFormat().resolvedOptions().locale;
      try {
        var gl = document.createElement('canvas').getContext('webgl');
        if (gl) {
          var dbg = gl.getExtension('WEBGL_debug_renderer_info');
          obs.webgl = {
            vendor: gl.getParameter(gl.VENDOR),
            renderer: gl.getParameter(gl.RENDERER),
            unmaskedVendor: dbg ? gl.getParameter(dbg.UNMASKED_VENDOR_WEBGL) : null,
            unmaskedRenderer: dbg ? gl.getParameter(dbg.UNMASKED_RENDERER_WEBGL) : null
          };
        }
      } catch (e) { obs.webgl = { error: String(e) }; }
      try {
        var c = document.createElement('canvas'); c.width = 240; c.height = 60;
        var x = c.getContext('2d');
        x.textBaseline = 'top'; x.font = '14px Arial';
        x.fillStyle = '#f60'; x.fillRect(100, 1, 62, 20);
        x.fillStyle = '#069'; x.fillText('essevin-capture', 2, 15);
        obs.canvasHash = c.toDataURL().slice(-64);
      } catch (e) { obs.canvasHash = 'err'; }
      obs.storage = {
        ls: localStorage.length, ss: sessionStorage.length,
        idb: (window.indexedDB && !!indexedDB.databases) ? 'yes' : 'n/a'
      };
    } catch (e) { obs.error = String(e); }
    return obs;
  }

  function send() {
    try {
      navigator.sendBeacon(HOOK, JSON.stringify({ kind: 'fingerprint', data: snapshot() }));
    } catch (e) {
      try {
        fetch(HOOK, { method: 'POST', body: JSON.stringify({ kind: 'fingerprint', data: snapshot() }) });
      } catch (e2) {}
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { setTimeout(send, 1500); });
  } else {
    setTimeout(send, 100);
  }

  // Late flush: navigator API touches collected during interaction.
  window.addEventListener('pagehide', function () {
    try {
      navigator.sendBeacon(HOOK, JSON.stringify({ kind: 'touch', data: req }));
    } catch (e) {}
  });
})();
