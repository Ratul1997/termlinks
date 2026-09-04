import assert from "node:assert/strict";
import { directAttachmentInput, insertAttachmentPath, shellQuotePath } from "./terminal-attachments";

assert.equal(
  shellQuotePath("/Users/ratul/Downloads/Termlinks Uploads/screen shot.png"),
  "'/Users/ratul/Downloads/Termlinks Uploads/screen shot.png'",
);
assert.equal(shellQuotePath("/tmp/that's-safe.png"), "'/tmp/that'\\''s-safe.png'");
assert.equal(directAttachmentInput("/tmp/screen shot.png"), " '/tmp/screen shot.png'");
assert.equal(directAttachmentInput("/tmp/that's-safe.png"), " '/tmp/that'\\''s-safe.png'");

assert.deepEqual(insertAttachmentPath("", "/tmp/a.png", 0, 0), {
  value: "'/tmp/a.png'",
  caret: 12,
});
assert.deepEqual(insertAttachmentPath("@codex inspect", "/tmp/screen shot.png", 15, 15), {
  value: "@codex inspect '/tmp/screen shot.png'",
  caret: 37,
});
assert.deepEqual(insertAttachmentPath("open now", "/tmp/report.pdf", 5, 5), {
  value: "open '/tmp/report.pdf' now",
  caret: 23,
});

console.log("terminal attachment path insertion passed");
