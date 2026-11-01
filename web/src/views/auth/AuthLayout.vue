<script setup lang="ts">
// Shared chrome for the primary auth pages (sign in, sign up): a brand panel on
// the left and a centered form column on the right, collapsing to the form alone
// on narrow screens.
//
// Only the chrome lives here. Form styling stays in the page that owns the form,
// because Vue's scoped CSS applies to slot content in the *parent's* scope, not
// this component's — styling slotted markup from here would need :slotted() and
// would couple the layout to each page's internals.
import { useThemeStore } from '../../stores/theme'

withDefaults(
  defineProps<{
    title: string
    subtitle?: string
    // Secondary pages (forgot / reset password) drop the brand panel and centre
    // the form on its own — the hero is an entry-point flourish, and repeating
    // it on a mid-flow step makes those pages feel heavier than they are.
    hero?: boolean
  }>(),
  { hero: true }
)

const theme = useThemeStore()
</script>

<template>
  <div class="auth" :class="{ 'auth-no-hero': !hero }">
    <!-- Brand panel. Decorative and duplicated by the form column's own
         heading, so it is hidden from assistive tech. -->
    <aside v-if="hero" class="auth-hero" aria-hidden="true">
      <div class="auth-hero-inner">
        <div class="auth-hero-wordmark">ReForwarder<span class="wm-dot">.</span></div>

        <div class="auth-hero-body">
          <h2 class="auth-hero-title">Email forwarding,<br />under your control.</h2>
          <p class="auth-hero-lead">
            Self-hosted email forwarding — manage domains, DNS authentication,
            and delivery from one place.
          </p>
          <ul class="auth-hero-features">
            <li><span class="mdi mdi-email-fast-outline"></span> Outbound &amp; inbound email</li>
            <li><span class="mdi mdi-file-document-multiple-outline"></span> Domains &amp; DNS authentication</li>
            <li><span class="mdi mdi-bullhorn-outline"></span> Templates &amp; campaigns</li>
            <li><span class="mdi mdi-webhook"></span> Inbound forwarding &amp; webhooks</li>
            <li><span class="mdi mdi-chart-areaspline"></span> Delivery analytics &amp; tracking</li>
            <li><span class="mdi mdi-account-group-outline"></span> Workspaces &amp; RBAC</li>
          </ul>
        </div>

        <p class="auth-hero-foot">Open-source · Self-hosted email forwarding</p>
      </div>
    </aside>

    <main class="auth-main">
      <div class="auth-card">
        <div class="auth-header">
          <div class="auth-wordmark" aria-label="ReForwarder">ReForwarder<span class="auth-wordmark-dot">.</span></div>
          <h1 class="auth-title">{{ title }}</h1>
          <p v-if="subtitle" class="auth-subtitle">{{ subtitle }}</p>
        </div>

        <slot />
      </div>
    </main>

    <button
      class="theme-btn"
      :title="theme.isDark ? 'Light mode' : 'Dark mode'"
      :aria-label="theme.isDark ? 'Switch to light mode' : 'Switch to dark mode'"
      @click="theme.toggle()"
    >
      <svg v-if="theme.isDark" width="18" height="18" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="3" stroke="currentColor" stroke-width="1.5"/><path d="M8 1v2M8 13v2M1 8h2M13 8h2M3.05 3.05l1.41 1.41M11.54 11.54l1.41 1.41M3.05 12.95l1.41-1.41M11.54 4.46l1.41-1.41" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
      <svg v-else width="18" height="18" viewBox="0 0 16 16" fill="none"><path d="M14 9.5A6.5 6.5 0 016.5 2 6.5 6.5 0 1014 9.5z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
    </button>
  </div>
</template>

<style scoped>
.auth {
  min-height: 100dvh;
  display: grid;
  grid-template-columns: 1.05fr 1fr;
  background: var(--bg-secondary);
  position: relative;
}

/* ─── Brand panel ─── */
/* The hero is a permanently-dark brand surface: fixed dark values, NOT theme
   tokens — the app sidebar flips with the theme, the hero does not. */
.auth-hero {
  position: relative;
  overflow: hidden;
  display: flex;
  min-height: 0;
  color: #fcfdff;
  background:
    radial-gradient(120% 80% at 100% 0%, rgba(255, 255, 255, 0.06), transparent 55%),
    radial-gradient(70% 55% at 8% 100%, var(--accent-glow), transparent 65%),
    #06060a;
}
/* Oversized wordmark watermark, echoing the lockup above it. */
.auth-hero::after {
  content: 'R';
  position: absolute;
  right: -4%;
  bottom: -22%;
  font-family: var(--font-display);
  font-size: 34rem;
  font-weight: 400;
  line-height: 1;
  color: #fcfdff;
  opacity: 0.04;
  pointer-events: none;
  user-select: none;
}
.auth-hero-inner {
  position: relative;
  z-index: 1;
  display: flex;
  flex-direction: column;
  width: 100%;
  max-width: 460px;
  margin: auto;
  min-height: 0;
  /* Vertical rhythm scales with the viewport so the hero fits 100dvh
     on short screens instead of pushing the footer below the fold. */
  padding: clamp(24px, 4.5vh, 56px) 52px;
}
.auth-hero-wordmark {
  align-self: flex-start;
  margin-bottom: auto;
  font-family: var(--font-display);
  font-size: 2.5rem;
  font-weight: 400;
  letter-spacing: -0.01em;
  line-height: 1;
  color: #fcfdff;
}
.auth-hero-wordmark .wm-dot { color: var(--accent); }

.auth-hero-body { margin: clamp(20px, 4.5vh, 48px) 0; }
/* Signature accent detail: a short orange rule with a faint glow. */
.auth-hero-body::before {
  content: '';
  display: block;
  width: 32px;
  height: 2px;
  border-radius: 1px;
  background: var(--accent);
  box-shadow: 0 0 16px var(--accent-glow);
  margin-bottom: clamp(14px, 2.2vh, 24px);
}
.auth-hero-title {
  font-family: var(--font-display);
  /* Width- AND height-aware: short viewports get a slightly tighter display
     size so the two-line headline never crowds the footer out of 100dvh. */
  font-size: clamp(1.9rem, 1.1vw + 1.6vh, 2.85rem);
  font-weight: 400;
  line-height: 1.08;
  letter-spacing: -0.01em;
  margin: 0 0 clamp(12px, 2vh, 16px);
}
.auth-hero-lead {
  font-size: 15px;
  line-height: 1.6;
  color: #888e90;
  max-width: 42ch;
  margin: 0 0 clamp(16px, 2.5vh, 28px);
}
.auth-hero-features {
  list-style: none;
  padding: clamp(12px, 2.5vh, 24px) 0 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: clamp(10px, 1.6vh, 14px);
  border-top: 1px solid rgba(255, 255, 255, 0.07);
}
.auth-hero-features li {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 14px;
  color: #888e90;
}
.auth-hero-features .mdi {
  font-size: 20px;
  flex-shrink: 0;
  width: 34px;
  height: 34px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius);
  border: 1px solid rgba(255, 255, 255, 0.07);
  background: rgba(255, 255, 255, 0.06);
  color: #fcfdff;
}
.auth-hero-foot {
  margin: 0;
  font-size: 11px;
  font-weight: 500;
  color: #888e90;
  text-transform: uppercase;
  letter-spacing: 0.08em;
}

/* ─── Form column ─── */
.auth-main {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 0;
  padding: clamp(24px, 5vh, 40px) 24px;
}

/* Dark mode: ONE continuous canvas spans the entire auth page. The hero's
   gradient language is extended to full-page coordinates (the hero occupies
   the left ~51%, so its gradient centers are re-mapped accordingly) and both
   columns render transparent over it — no panel seam, no two-tone split.
   Light mode keeps the hero as a dark brand panel beside the light form. */
[data-theme="dark"] .auth {
  background:
    radial-gradient(68% 90% at 56% -8%, rgba(255, 255, 255, 0.06), transparent 55%),
    radial-gradient(38% 58% at 4% 105%, var(--accent-glow), transparent 65%),
    #06060a;
}

[data-theme="dark"] .auth-hero {
  background: none;
}

[data-theme="dark"] .auth-main {
  background: none;
}
.auth-card {
  width: 100%;
  max-width: 380px;
}
.auth-header { text-align: center; margin-bottom: 24px; }
.auth-wordmark {
  font-family: var(--font-display);
  font-size: 30px;
  font-weight: 400;
  letter-spacing: -0.01em;
  color: var(--text-primary);
  margin-bottom: 20px;
  line-height: 1;
}
.auth-wordmark-dot {
  color: var(--accent);
  margin-left: 1px;
}
.auth-title {
  font-size: 22px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: -0.02em;
  margin: 0 0 8px;
}
.auth-subtitle {
  font-size: 14px;
  color: var(--text-muted);
  margin: 0;
}

.theme-btn {
  position: fixed;
  top: 20px;
  right: 20px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  padding: 10px;
  cursor: pointer;
  color: var(--text-tertiary);
  display: flex;
  align-items: center;
  transition: all var(--transition);
  box-shadow: var(--shadow-sm);
}
.theme-btn:hover { color: var(--text-primary); border-color: var(--border-input); }

.auth-no-hero { grid-template-columns: 1fr; }

/* ─── Responsive: collapse to a single centered form ─── */
@media (max-width: 900px) {
  .auth { grid-template-columns: 1fr; }
  .auth-hero { display: none; }
}
</style>
