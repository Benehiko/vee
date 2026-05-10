(function () {
  const LINES = [
    '01101001', '10110110', '11001010', '00110101',
    '10101110', '01110010', '11100010', '00101110',
    '01001101', '10011101', '11010011', '00011011',
    '>_INIT▓░', '>_SEGFLT', '>_██░▒▓█', '>_KVM:HLT',
    'ERR::0xFE', 'ERR::0x00', 'ERR::NULL', 'ERR::VFIO',
    'VFIO BIND', 'VFIO:PASS', 'PCIe:BIND', 'IOMMU:ON ',
    'KVM:READY', 'KVM:SMP  ', 'KVM:HLT  ', 'SSH:23▓░█',
    'MEM:16GiB', 'DMA:MAP▒ ', 'ROM:DUMP▓', 'GPU:PASS ',
    'QEMU::RUN', 'SPICE:OFF', 'VEE::BOOT', 'VEE::STOP',
    '▓▒░██▓▒░', '░▒▓██▓▒░', '▓█░▒ERR▓', '░▒▓01101',
  ];

  const FONT_SIZE = 13; // px
  const LINE_HEIGHT = 22; // px
  const OPACITY_MIN = 0.06;
  const OPACITY_MAX = 0.22;
  const FLICKER_INTERVAL_MIN = 80;  // ms per line update
  const FLICKER_INTERVAL_MAX = 300;

  function randInt(a, b) { return Math.floor(Math.random() * (b - a + 1)) + a; }
  function randItem(arr) { return arr[randInt(0, arr.length - 1)]; }
  function randFloat(a, b) { return Math.random() * (b - a) + a; }

  function createColumn(side) {
    const col = document.createElement('div');
    col.className = 'glitch-col glitch-col--' + side;
    col.setAttribute('aria-hidden', 'true');

    Object.assign(col.style, {
      position: 'fixed',
      top: '0',
      bottom: '0',
      [side]: '0',
      width: '3rem',
      overflow: 'hidden',
      pointerEvents: 'none',
      zIndex: '50',
      display: 'flex',
      flexDirection: 'column',
      gap: '0',
      fontFamily: "'Share Tech Mono', monospace",
      fontSize: FONT_SIZE + 'px',
      lineHeight: LINE_HEIGHT + 'px',
      color: '#00ffe1',
      writingMode: 'vertical-rl',
      textOrientation: 'mixed',
      whiteSpace: 'nowrap',
      userSelect: 'none',
    });

    document.body.appendChild(col);
    return col;
  }

  function populateColumn(col) {
    const h = window.innerHeight;
    const count = Math.ceil(h / LINE_HEIGHT) + 2;
    col.innerHTML = '';
    for (let i = 0; i < count; i++) {
      const span = document.createElement('span');
      span.textContent = randItem(LINES);
      span.style.opacity = randFloat(OPACITY_MIN, OPACITY_MAX).toFixed(2);
      span.style.display = 'block';
      span.style.height = LINE_HEIGHT + 'px';
      col.appendChild(span);
    }
  }

  function startFlicker(col) {
    const spans = col.querySelectorAll('span');
    function tick() {
      // Update a random subset of lines each tick
      const count = randInt(1, 4);
      for (let i = 0; i < count; i++) {
        const span = spans[randInt(0, spans.length - 1)];
        span.textContent = randItem(LINES);
        span.style.opacity = randFloat(OPACITY_MIN, OPACITY_MAX).toFixed(2);
      }
      setTimeout(tick, randInt(FLICKER_INTERVAL_MIN, FLICKER_INTERVAL_MAX));
    }
    tick();
  }

  function init() {
    const left = createColumn('left');
    const right = createColumn('right');
    populateColumn(left);
    populateColumn(right);
    startFlicker(left);
    startFlicker(right);

    window.addEventListener('resize', function () {
      populateColumn(left);
      populateColumn(right);
    });
  }

  function initScrollHeader() {
    var header = document.querySelector('.gdoc-header');
    if (!header) return;
    var lastY = window.scrollY;
    var ticking = false;

    window.addEventListener('scroll', function () {
      if (!ticking) {
        window.requestAnimationFrame(function () {
          var y = window.scrollY;
          if (y < 10) {
            header.classList.remove('header--hidden');
          } else if (y > lastY) {
            header.classList.add('header--hidden');
          } else {
            header.classList.remove('header--hidden');
          }
          lastY = y;
          ticking = false;
        });
        ticking = true;
      }
    }, { passive: true });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { init(); initScrollHeader(); });
  } else {
    init();
    initScrollHeader();
  }
})();
