'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { normalizeProxyUrl } = require('../public/validation.js');

test('empty proxy URL is allowed', () => {
  assert.equal(normalizeProxyUrl(''), '');
  assert.equal(normalizeProxyUrl('   '), '');
});

test('http and https URLs are normalized', () => {
  assert.equal(normalizeProxyUrl(' https://example.com/sub?token=test '), 'https://example.com/sub?token=test');
  assert.equal(normalizeProxyUrl('http://127.0.0.1:8080/sub'), 'http://127.0.0.1:8080/sub');
  assert.equal(normalizeProxyUrl('https://example.com:65535/sub'), 'https://example.com:65535/sub');
});

test('malformed or incomplete URLs are rejected', () => {
  for (const value of [
    'example.com/sub', '/sub', 'ftp://example.com/sub', 'https://',
    'https://exa mple.com/sub', 'https://example.com/%zz',
    'https://example.com:0/sub', 'https://example.com:65536/sub',
    'javascript:alert(1)', 'https://example.com/' + 'a'.repeat(4096),
    null, undefined, 42,
  ]) {
    assert.throws(() => normalizeProxyUrl(value), /URL 格式错误/);
  }
});
