'use client';

import * as React from 'react';
import { jsx as _jsx } from "react/jsx-runtime";
export const DEFAULT_MODE_STORAGE_KEY = 'mode';
export const DEFAULT_COLOR_SCHEME_STORAGE_KEY = 'color-scheme';
export const DEFAULT_ATTRIBUTE = 'data-color-scheme';
// React 17 has no `useSyncExternalStore`; spread into a plain object so reading the
// missing property returns `undefined` instead of throwing under strict ESM. See #41190 (comment).
const safeReact = {
  ...React
};
const maybeReactUseSyncExternalStore = safeReact.useSyncExternalStore;
const subscribe = () => () => {};

// Serialize a value into a JS string literal safe to embed in the inline script. JSON.stringify
// escapes quotes and backslashes; the extra replaces handle what it does not: `<` (so `</script>`
// can't close the element) and the U+2028/U+2029 line separators, invalid raw inside a JS string.
function serializeScriptValue(value) {
  return JSON.stringify(value).replace(/</g, '\\u003c').replace(/\u2028/g, '\\u2028').replace(/\u2029/g, '\\u2029');
}

// Insert a runtime scheme variable (`light`, `dark`, or `colorScheme`) into every `%s` placeholder
// of an attribute/selector template.
function interpolateScheme(template, variable) {
  const segments = template.split('%s');
  const tokens = [];
  for (let index = 0; index < segments.length; index += 1) {
    if (index > 0) {
      tokens.push(variable);
    }
    if (segments[index]) {
      tokens.push(serializeScriptValue(segments[index]));
    }
  }
  return tokens.join(' + ') || '""';
}

/**
 * `true` during the server render and the matching hydration render, `false`
 * on every client render afterwards. React warns when a `<script>` is
 * created during a client render (such scripts never execute), so the inline
 * script is only emitted on the server pass and dropped after hydration — the
 * attribute it already set on the document persists. React <18 has no
 * `useSyncExternalStore` and no such warning, so the script is always emitted.
 */
function useIsServerRender() {
  if (maybeReactUseSyncExternalStore === undefined) {
    return true;
  }
  return maybeReactUseSyncExternalStore(subscribe, () => false, () => true);
}
export function buildInitColorSchemeScript(options) {
  const {
    defaultMode = 'system',
    defaultLightColorScheme = 'light',
    defaultDarkColorScheme = 'dark',
    modeStorageKey = DEFAULT_MODE_STORAGE_KEY,
    colorSchemeStorageKey = DEFAULT_COLOR_SCHEME_STORAGE_KEY,
    attribute: initialAttribute = DEFAULT_ATTRIBUTE,
    colorSchemeNode = 'document.documentElement',
    nonce
  } = options || {};
  let setter = '';
  let attribute = initialAttribute;
  if (initialAttribute === 'class') {
    attribute = '.%s';
  }
  if (initialAttribute === 'data') {
    attribute = '[data-%s]';
  }
  if (attribute.startsWith('.')) {
    const selector = attribute.substring(1);
    setter += `${colorSchemeNode}.classList.remove(${interpolateScheme(selector, 'light')}, ${interpolateScheme(selector, 'dark')});
      ${colorSchemeNode}.classList.add(${interpolateScheme(selector, 'colorScheme')});`;
  }
  const matches = attribute.match(/\[([^[\]]+)\]/); // case [data-color-scheme='%s'] or [data-color-scheme]
  if (matches) {
    const separator = matches[1].indexOf('=');
    const attr = separator === -1 ? matches[1] : matches[1].slice(0, separator);
    const value = separator === -1 ? undefined : matches[1].slice(separator + 1);
    if (!value) {
      setter += `${colorSchemeNode}.removeAttribute(${interpolateScheme(attr, 'light')});
      ${colorSchemeNode}.removeAttribute(${interpolateScheme(attr, 'dark')});`;
    }
    const attributeValue = value ? interpolateScheme(value.replace(/^(['"])(.*)\1$/, '$2'), 'colorScheme') : '""';
    setter += `
      ${colorSchemeNode}.setAttribute(${interpolateScheme(attr, 'colorScheme')}, ${attributeValue});`;
  } else if (!attribute.startsWith('.')) {
    setter += `${colorSchemeNode}.setAttribute(${serializeScriptValue(attribute)}, colorScheme);`;
  }
  return /*#__PURE__*/_jsx("script", {
    suppressHydrationWarning: true,
    nonce: typeof window === 'undefined' ? nonce : ''
    // eslint-disable-next-line react/no-danger
    ,
    dangerouslySetInnerHTML: {
      __html: `(function() {
try {
  let colorScheme = '';
  const mode = localStorage.getItem(${serializeScriptValue(modeStorageKey)}) || ${serializeScriptValue(defaultMode)};
  const dark = localStorage.getItem(${serializeScriptValue(`${colorSchemeStorageKey}-dark`)}) || ${serializeScriptValue(defaultDarkColorScheme)};
  const light = localStorage.getItem(${serializeScriptValue(`${colorSchemeStorageKey}-light`)}) || ${serializeScriptValue(defaultLightColorScheme)};
  if (mode === 'system') {
    // handle system mode
    const mql = window.matchMedia('(prefers-color-scheme: dark)');
    if (mql.matches) {
      colorScheme = dark
    } else {
      colorScheme = light
    }
  }
  if (mode === 'light') {
    colorScheme = light;
  }
  if (mode === 'dark') {
    colorScheme = dark;
  }
  if (colorScheme) {
    ${setter}
  }
} catch(e){}})();`
    }
  }, "mui-color-scheme-init");
}
export default function InitColorSchemeScript(options) {
  const isServerRender = useIsServerRender();
  if (!isServerRender) {
    return null;
  }
  return buildInitColorSchemeScript(options);
}