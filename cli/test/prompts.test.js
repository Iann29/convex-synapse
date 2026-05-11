const assert = require("node:assert/strict");
const test = require("node:test");

const { parseCredentialsInput } = require("../lib/prompts");

test("parseCredentialsInput reads email and password from piped stdin text", () => {
  assert.deepEqual(parseCredentialsInput("ian@example.com\nstrongpass123\n"), {
    email: "ian@example.com",
    password: "strongpass123",
  });
});

test("parseCredentialsInput preserves password spaces", () => {
  assert.deepEqual(parseCredentialsInput(" ian@example.com \n pass with spaces \n"), {
    email: "ian@example.com",
    password: " pass with spaces ",
  });
});
