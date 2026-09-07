// Runs before first paint. Both the theme and the strip resolution are
// remembered per visitor, and applying them after paint would flash the wrong
// theme and then visibly swap the strips underneath the reader.
(function () {
  var d = document.documentElement;
  try {
    var t = localStorage.getItem('is-ntust-down:theme');
    if (t === 'light' || t === 'dark') d.setAttribute('data-theme', t);

    // "auto" is stored explicitly rather than as an absent key: someone on a
    // dark-mode OS who deliberately chose light must stay light, and that is
    // indistinguishable from "never chose" if absence is the only signal.
    var r = localStorage.getItem('is-ntust-down:range');
    if (r === 'day' || r === 'hour') d.setAttribute('data-range', r);
  } catch (e) {
    /* private mode, blocked storage — fall back to the markup defaults */
  }
})();
