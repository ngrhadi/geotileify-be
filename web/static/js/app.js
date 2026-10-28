/* Geotileify landing interactions.
 * 1) Health: one /health probe drives the nav pill + footer status.
 * 2) Header: scrolled shadow, scroll-spy active link, mobile menu.
 * 3) Hero tile texture: faint uneven field rendered once; a whisper of
 *    brand cells breathes very slowly — background, never animation.
 * 4) Scrollcraft-lite: IntersectionObserver reveal for bands/cards. */
(() => {
  "use strict";

  // ---- 0) icon fallback: hide <img class="icon"> that 404s ----
  // (Lucide SVGs are added manually to web/static/icons/; until then,
  // suppress the broken-image glyph so layout stays clean.)
  document.addEventListener(
    "error",
    (e) => {
      const t = e.target;
      if (t instanceof HTMLImageElement && t.classList.contains("icon")) {
        t.style.display = "none";
      }
    },
    true
  );

  // ---- 1) health (nav pill + footer status share one probe) ----
  const badge = document.getElementById("healthBadge");
  const healthText = document.getElementById("healthText");
  const footerHealth = document.getElementById("footerHealth");
  const footerHealthWrap = document.getElementById("footerHealthWrap");
  fetch("/health", { cache: "no-store" })
    .then((r) => (r.ok ? r.json() : Promise.reject(new Error("unhealthy"))))
    .then(() => {
      if (badge) badge.classList.add("is-ok");
      if (healthText) healthText.textContent = "Operational";
      if (footerHealth) footerHealth.textContent = "All systems operational";
      if (footerHealthWrap) footerHealthWrap.classList.add("is-ok");
    })
    .catch(() => {
      if (badge) badge.classList.add("is-err");
      if (healthText) healthText.textContent = "Unreachable";
      if (footerHealth) footerHealth.textContent = "Service unreachable";
      if (footerHealthWrap) footerHealthWrap.classList.add("is-err");
    });

  // ---- 2a) header shadow on scroll (rAF-throttled) ----
  const nav = document.getElementById("siteNav");
  if (nav) {
    let ticking = false;
    const onScroll = () => {
      nav.classList.toggle("is-scrolled", window.scrollY > 8);
      ticking = false;
    };
    window.addEventListener(
      "scroll",
      () => {
        if (!ticking) {
          ticking = true;
          requestAnimationFrame(onScroll);
        }
      },
      { passive: true }
    );
    onScroll();
  }

  // ---- 2b) mobile menu ----
  const menuBtn = document.getElementById("menuBtn");
  const mobileMenu = document.getElementById("mobileMenu");
  if (menuBtn && mobileMenu) {
    const close = () => {
      mobileMenu.hidden = true;
      menuBtn.setAttribute("aria-expanded", "false");
      menuBtn.setAttribute("aria-label", "Open menu");
    };
    menuBtn.addEventListener("click", () => {
      const open = mobileMenu.hidden;
      mobileMenu.hidden = !open;
      menuBtn.setAttribute("aria-expanded", String(open));
      menuBtn.setAttribute("aria-label", open ? "Close menu" : "Open menu");
    });
    mobileMenu.querySelectorAll("a").forEach((a) =>
      a.addEventListener("click", close)
    );
    window.addEventListener("resize", () => {
      if (window.innerWidth > 900) close();
    });
  }

  // ---- 2c) scroll-spy: highlight the section at the top rail ----
  // IntersectionObserver with a narrow middle band fails for the short
  // #formats strip (Features fills the screen and wins), so use the section
  // whose top has most recently crossed the activation line instead.
  const spyLinks = document.querySelectorAll(".nav-links a[data-spy]");
  if (spyLinks.length) {
    const sections = [];
    spyLinks.forEach((a) => {
      const el = document.getElementById(a.dataset.spy);
      if (el) sections.push({ id: el.id, el });
    });
    // Nav order is Features → Endpoints → Formats; sort by document position.
    sections.sort(
      (a, b) =>
        a.el.getBoundingClientRect().top - b.el.getBoundingClientRect().top
    );

    const setActive = (id) => {
      spyLinks.forEach((a) =>
        a.classList.toggle("is-active", a.dataset.spy === id)
      );
    };

    let spyTicking = false;
    const updateSpy = () => {
      spyTicking = false;
      // Just below the sticky nav + 88px scroll-padding-top.
      const line = 112;
      let activeId = null;
      sections.forEach((s) => {
        if (s.el.getBoundingClientRect().top <= line) activeId = s.id;
      });
      setActive(activeId);
    };
    const requestSpy = () => {
      if (!spyTicking) {
        spyTicking = true;
        requestAnimationFrame(updateSpy);
      }
    };
    window.addEventListener("scroll", requestSpy, { passive: true });
    window.addEventListener("resize", requestSpy);
    updateSpy();
  }

  // ---- 3) hero canvas: tile-grid + light-blue pulse wave ----
  // Field tile putih/abu tetap merata. Accent cells biru muda berdenyut
  // dan sengaja dimulai dari sisi kanan (di luar scrim copy) agar animasi
  // hero terlihat; fasenya membentuk gelombang dari kanan ke kiri.
  const canvas = document.getElementById("tileCanvas");
  if (canvas && canvas.getContext) {
    const ctx = canvas.getContext("2d");
    const TILE = 34; // logical tile size (px @ dpr 1)
    let cells = [];
    let W = 0;
    let H = 0;

    const resize = () => {
      const rect = canvas.getBoundingClientRect();
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      W = Math.max(1, Math.round(rect.width));
      H = Math.max(1, Math.round(rect.height));
      canvas.width = Math.round(rect.width * dpr);
      canvas.height = Math.round(rect.height * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      cells = buildField(W, H);
      paint(0);
    };

    // Bangun field sekali per resize: relief 3-oktaf + speckle untuk tile
    // putih statis, lalu accent cells biru muda. Density accent naik ke
    // arah kanan supaya denyutnya jelas terlihat, bukan tertutup copy.
    const buildField = (w, h) => {
      const cols = Math.ceil(w / TILE) + 1;
      const rows = Math.ceil(h / TILE) + 1;
      const out = [];
      for (let r = 0; r < rows; r++) {
        for (let c = 0; c < cols; c++) {
          const n =
            0.55 * hash(c * 0.3, r * 0.29) +
            0.3 * hash(c * 0.12 + 7.3, r * 0.14 + 3.1) +
            0.15 * hash(c * 0.55 + 1.7, r * 0.52 + 9.2);
          const m = hash(c * 0.37 + r * 1.13, 11.1);
          const leftness = 1 - c / cols;
          if (m > 0.68 + leftness * 0.22) continue; // speckle holes
          let tone = "";
          if (n > 0.66) tone = "t2";
          else if (n > 0.54) tone = "t1";
          else if (hash(c * 0.9, r * 0.7) > 0.8) tone = "t1";
          else continue;

          // Accent hanya di bagian kanan (di luar copy), density naik ke tepi.
          const rightness = c / cols;
          const p =
            rightness > 0.52 ? 0.02 + (rightness - 0.52) * 0.44 : 0;
          const pulse = hash(c * 0.77 + r * 0.31, 4.2) < p;

          // Fase gelombang merambat dari kanan ke kiri.
          const wave = c * 0.24 + r * 0.14;
          out.push({
            x: c * TILE,
            y: r * TILE,
            tone,
            pulse,
            phase: wave + hash(c * 1.7, r * 2.3) * 0.7,
            period: 5600,
          });
        }
      }
      return out;
    };

    // Static field abu-hangat; accent pulse biru muda (#9EE0F8 → #60B2E0).
    const TONES = {
      t1: [226, 235, 235],
      t2: [200, 220, 220],
      lit: [158, 224, 248],
      deep: [96, 178, 224],
    };
    const paint = (now) => {
      ctx.clearRect(0, 0, W, H);
      // Wash vertikal yang seragam secara horizontal.
      const wash = ctx.createLinearGradient(0, 0, 0, H);
      wash.addColorStop(0, "#263636");
      wash.addColorStop(1, "#1b2424");
      ctx.fillStyle = wash;
      ctx.fillRect(0, 0, W, H);
      // Glow brand di tengah, simetris kiri/kanan.
      const glow = ctx.createRadialGradient(
        W * 0.5,
        H * 0.38,
        0,
        W * 0.5,
        H * 0.38,
        Math.max(W, H) * 0.6
      );
      glow.addColorStop(0, "rgba(142,210,212,0.10)");
      glow.addColorStop(1, "rgba(142,210,212,0)");
      ctx.fillStyle = glow;
      ctx.fillRect(0, 0, W, H);
      const gap = 3;
      cells.forEach((cell) => {
        let rgb = TONES[cell.tone] || TONES.t1;
        let alpha = 0.40;
        let inset = 0;
        let glow = false;

        if (cell.pulse) {
          // Napas biru muda; fase gelombang membuat denyut merambat
          // dari kanan ke kiri. Ukuran ikut membesar saat terang.
          const s =
            (Math.sin((now / cell.period) * Math.PI * 2 + cell.phase) + 1) / 2;
          rgb = mix(TONES.lit, TONES.deep, s);
          alpha = 0.16 + s * 0.68;
          inset = (1 - s) * 2.2;
          ctx.shadowColor = "rgba(158,224,248,0.55)";
          ctx.shadowBlur = 6 + s * 10;
          glow = true;
        }

        ctx.fillStyle = `rgba(${rgb[0] | 0},${rgb[1] | 0},${rgb[2] | 0},${alpha})`;
        const g = gap + inset;
        roundRect(cell.x + g / 2, cell.y + g / 2, TILE - g, TILE - g, 3);
        if (glow) {
          ctx.shadowBlur = 0;
          ctx.shadowColor = "transparent";
        }
      });
    };

    const roundRect = (x, y, w, h, r) => {
      ctx.beginPath();
      ctx.moveTo(x + r, y);
      ctx.arcTo(x + w, y, x + w, y + h, r);
      ctx.arcTo(x + w, y + h, x, y + h, r);
      ctx.arcTo(x, y + h, x, y, r);
      ctx.arcTo(x, y, x + w, y, r);
      ctx.closePath();
      ctx.fill();
    };

    const mix = (a, b, t) => [
      a[0] + (b[0] - a[0]) * t,
      a[1] + (b[1] - a[1]) * t,
      a[2] + (b[2] - a[2]) * t,
    ];

    const reduceMotion = window.matchMedia(
      "(prefers-reduced-motion: reduce)"
    ).matches;
    if (reduceMotion) {
      paint(1e9); // satu frame statis
    } else {
      let raf = 0;
      let last = 0;
      const loop = (now) => {
        // ~12fps cukup untuk napas lambat; hemat baterai
        if (now - last > 80) {
          last = now;
          paint(now);
        }
        raf = requestAnimationFrame(loop);
      };
      raf = requestAnimationFrame(loop);
      document.addEventListener("visibilitychange", () => {
        if (document.hidden) cancelAnimationFrame(raf);
        else {
          last = 0;
          raf = requestAnimationFrame(loop);
        }
      });
    }
    let resizeTimer = 0;
    window.addEventListener("resize", () => {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(resize, 150);
    });
    resize();
  }

  // ---- 4) scrollcraft-lite: staggered reveal on scroll ----
  const revealEls = document.querySelectorAll(
    ".band, .strip, .card, .endpoint, .footer-top"
  );
  if ("IntersectionObserver" in window && revealEls.length) {
    revealEls.forEach((el) => el.classList.add("reveal"));
    const io = new IntersectionObserver(
      (entries) => {
        entries.forEach((en) => {
          if (en.isIntersecting) {
            en.target.classList.add("revealed");
            io.unobserve(en.target);
          }
        });
      },
      { threshold: 0.12, rootMargin: "0px 0px -8% 0px" }
    );
    revealEls.forEach((el) => io.observe(el));
  }

  function hash(x, y) {
    const s = Math.sin(x * 127.1 + y * 311.7) * 43758.5453;
    return s - Math.floor(s);
  }
})();
