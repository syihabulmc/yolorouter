<!-- Shared shell for the setup/login pages (../views/auth/*.vue):
     a split layout with a dark brand panel on the left (product identity)
     and the page's own form on the right. Neither page renders inside
     DefaultLayout (they're top-level routes, not its children — see
     router/index.ts), so DefaultLayout's own locale <select> never reaches
     them; this is the only other place a language switcher can live, and
     both pages require one. Props (title/subtitle) + default slot keep the
     same API, so LoginPage/SetupPage need no changes. -->
<template>
  <div class="auth-page">
    <div class="auth-hero-warp">
      <img :src="loginBg" alt="" class="auth-hero-placeholder">
      <div class="auth-hero">
        <img class="auth-hero-logo" :src="logo2" alt="" width="380" />
        <img class="auth-hero-logo-mark" :src="titleImage" alt="" width="225" />
        <div>
          <div class="auth-hero-title">{{ t("auth.brandTagline") }}</div>
          <div class="auth-hero-subtitle">
            {{ t("auth.brandDesc") }}
          </div>
          <div class="auth-hero-feature">
            {{ t("auth.brandPoint1") }}
          </div>
          <div class="auth-hero-feature">
            {{ t("auth.brandPoint2") }}
          </div>
          <div class="auth-hero-feature">
            {{ t("auth.brandPoint3") }}
          </div>
        </div>
      </div>
    </div>
    <div class="auth-main">
      <div class="auth-locale-select">
        <LocaleSwitcher />
      </div>
      <div class="auth-card-wrap">
        <div class="auth-card">
          <div
            class="auth-card-logo"
            :style="`background-image: url(${banner});`"
          >
            <img :src="logo3" alt="" width="74" />
            <img
              :src="titleBlack"
              alt=""
              width="131"
              style="margin-left: 16px"
            />
          </div>
          <div class="auth-card-body">
            <h1 class="auth-title">{{ title }}</h1>
            <p class="auth-subtitle">{{ subtitle }}</p>
            <p class="auth-subtitle-2">{{ t("auth.username") }}</p>
            <slot />
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from "vue-i18n";
import LocaleSwitcher from "./LocaleSwitcher.vue";
import logo2 from "../assets/logo-2.png";
import logo3 from "../assets/logo-3.png";
import banner from "../assets/banner.png";
import loginBg from "../assets/login-bg.png";
import titleImage from "../assets/title.png";
import titleBlack from "../assets/title-black.png";

defineProps<{ title: string; subtitle: string }>();
const { t } = useI18n();
</script>

<style scoped>
.auth-page {
  display: flex;
  width: 100vw;
  height: 100vh;
  background-color: #fff;
}
.auth-hero-warp {
  position: relative;
  flex: 1;
}
.auth-hero-placeholder {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.auth-hero {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  justify-content: center;
  align-items: center;
  background-repeat: no-repeat;
  background-size: 100% auto;
  background-position: bottom center;
  color: #333;
}
.auth-main {
  display: flex;
  align-items: center;
  padding: 80px;
  background-color: #fff;
}

.auth-hero-logo {
  margin-bottom: 10px;
}
.auth-hero-logo-mark {
  margin-top: -100px;
}
.auth-hero-title {
  font-size: 36px;
  font-weight: bold;
  margin-top: 20px;
  margin-bottom: 5px;
  color: #000;
}
.auth-hero-subtitle {
  font-weight: bold;
  margin-bottom: 15px;
  font-size: 14px;
}
.auth-hero-feature {
  position: relative;
  padding-left: 8px;
  color: #666;
  margin-top: 4px;
}

.auth-hero-feature::after {
  content: "";
  position: absolute;
  top: 10px;
  left: 0;
  display: inline-block;
  width: 4px;
  height: 4px;
  background-color: #666;
  border-radius: 50%;
  vertical-align: middle;
}
.auth-card-logo {
  display: flex;
  justify-content: center;
  align-items: center;
  background-repeat: no-repeat;
  background-size: 100% auto;
  background-position: top center;
  height: 140px;
  margin-bottom: 45px;
}

.auth-card-wrap {
  width: 590px;
  background: #ffffff;
  box-shadow: 0px 1px 9px 1px rgba(109, 109, 103, 0.19);
  border-radius: 9px;
  padding: 2px;
  overflow: hidden;
}

.auth-card-body {
  padding: 0 80px 80px;
}

.auth-locale-select {
  position: absolute;
  top: var(--space-5);
  right: var(--space-6);
}

.auth-title {
  margin: 0;
  font-size: var(--text-2xl);
  font-weight: 800;
  line-height: 1.15;
  letter-spacing: -0.01em;
  color: #333;
}

.auth-subtitle {
  margin: var(--space-2) 0 0;
  color: #666;
  font-size: 12px;
}
.auth-subtitle-2 {
  margin-top: 32px;
  font-weight: bold;
  font-size: 18px;
  color: #102c44;
}

@media (max-width: 1300px) {
  .auth-hero-title {
    font-size: 18px;
  }
  .auth-hero-subtitle,.auth-hero-feature {
    font-size: 12px;
  }
  .auth-main {
    padding: 20px;
  }
}

/* ---- Mobile: hide the brand panel and let the form card fill the width ---- */
@media (max-width: 1000px) {
  .auth-page {
    flex-direction: column;
    width: 100%;
    height: auto;
    min-height: 100vh;
  }
  .auth-hero-warp,
  .auth-hero {
    display: none;
  }
  .auth-main {
    /* Reserve top room for the absolute locale switcher so it never overlaps
       the card at narrow widths, and drop the desktop 80px inset that would
       overflow a phone viewport. */
    width: 100%;
    padding: 64px 16px 24px;
    box-sizing: border-box;
    justify-content: center;
  }
  .auth-card-wrap {
    width: 100%;
    max-width: 420px;
    margin: 0 auto;
  }
  .auth-card-logo {
    height: 100px;
    margin-bottom: 28px;
  }
  .auth-card-body {
    padding: 0 24px 40px;
  }
}
</style>
<style>
.auth-page .n-form-item.n-form-item--medium-size.n-form-item--top-labelled {
  --n-label-height: 0 !important;
}

.auth-page .n-input-wrapper {
  background-color: #f9f9f9;
  border: 0;
}

.auth-page .n-input__state-border,
.auth-page .n-input__border {
  display: none;
}

.auth-page .n-input-wrapper {
  --n-height: 50px;
}

.auth-page .n-form-item-feedback-wrapper {
  margin-bottom: 20px !important
}
</style>
