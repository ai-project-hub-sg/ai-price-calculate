(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  if (root) root.AipcValidation = api;
})(typeof globalThis !== 'undefined' ? globalThis : null, function () {
  'use strict';

  function normalizeProxyUrl(value) {
    if (typeof value !== 'string') throw new Error('URL 格式错误');
    const raw = value.trim();
    if (!raw) return '';
    if (raw.length > 4096 || /\s/.test(raw) || /%(?![0-9a-f]{2})/i.test(raw)) throw new Error('URL 格式错误');
    let parsed;
    try {
      parsed = new URL(raw);
    } catch (_) {
      throw new Error('URL 格式错误');
    }
    if (!['http:', 'https:'].includes(parsed.protocol) || !parsed.hostname) {
      throw new Error('URL 格式错误');
    }
    if (parsed.port && (Number(parsed.port) < 1 || Number(parsed.port) > 65535)) {
      throw new Error('URL 格式错误');
    }
    return raw;
  }

  return { normalizeProxyUrl };
});
