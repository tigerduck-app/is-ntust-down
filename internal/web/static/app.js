(function () {
  'use strict';

  var KEY = 'is-ntust-down:theme';
  var MODES = ['auto', 'light', 'dark'];
  var ICONS = { auto: '◐', light: '☀', dark: '☾' };

  // Labels come from the button's data attributes so the server, which alone
  // knows the negotiated language, stays the single source of copy.
  function labels(button) {
    return {
      auto: button.getAttribute('data-label-auto') || 'System',
      light: button.getAttribute('data-label-light') || 'Light',
      dark: button.getAttribute('data-label-dark') || 'Dark'
    };
  }

  function read() {
    try {
      var v = localStorage.getItem(KEY);
      return MODES.indexOf(v) === -1 ? 'auto' : v;
    } catch (e) {
      return 'auto';
    }
  }

  function write(mode) {
    try {
      localStorage.setItem(KEY, mode);
    } catch (e) {
      /* Blocked storage still gets a working toggle for this page view. */
    }
  }

  function apply(mode, button) {
    if (mode === 'auto') {
      document.documentElement.removeAttribute('data-theme');
    } else {
      document.documentElement.setAttribute('data-theme', mode);
    }
    if (!button) return;
    var text = labels(button)[mode];
    button.querySelector('.theme__icon').textContent = ICONS[mode];
    button.querySelector('.theme__label').textContent = text;
    button.setAttribute('aria-label',
      (button.getAttribute('data-label-prefix') || 'Appearance') + ': ' + text);
  }

  function initTheme() {
    var button = document.getElementById('theme-toggle');
    var mode = read();
    apply(mode, button);
    if (!button) return;

    button.addEventListener('click', function () {
      mode = MODES[(MODES.indexOf(mode) + 1) % MODES.length];
      write(mode);
      apply(mode, button);
    });
  }

  // Tooltips do not exist on touch devices, so the same per-day information
  // has to be reachable by tapping a bar.
  function initStrips() {
    document.querySelectorAll('.strip').forEach(function (strip) {
      var detail = strip.querySelector('.strip__detail');
      if (!detail) return;
      var placeholder = detail.getAttribute('data-placeholder') || '';

      strip.querySelectorAll('.bar').forEach(function (bar) {
        bar.addEventListener('click', function () {
          var active = bar.getAttribute('aria-pressed') === 'true';
          strip.querySelectorAll('.bar[aria-pressed="true"]').forEach(function (b) {
            b.setAttribute('aria-pressed', 'false');
          });
          if (active) {
            detail.textContent = placeholder;
            return;
          }
          bar.setAttribute('aria-pressed', 'true');
          detail.textContent = bar.getAttribute('data-detail') || placeholder;
        });
      });
    });
  }

  function initRange() {
    var KEY = 'is-ntust-down:range';
    var buttons = document.querySelectorAll('.rangebtn');
    if (!buttons.length) return;

    function current() {
      return document.documentElement.getAttribute('data-range') === 'day' ? 'day' : 'hour';
    }

    function paint() {
      var active = current();
      buttons.forEach(function (b) {
        b.setAttribute('aria-pressed', String(b.getAttribute('data-range') === active));
      });
    }

    buttons.forEach(function (b) {
      b.addEventListener('click', function () {
        var next = b.getAttribute('data-range');
        document.documentElement.setAttribute('data-range', next);
        try {
          localStorage.setItem(KEY, next);
        } catch (e) {
          /* blocked storage still gets a working toggle for this page view */
        }
        paint();
      });
    });

    paint();
  }

  initTheme();
  initRange();
  initStrips();
})();
