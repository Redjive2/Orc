// Line endings.
//
// A textarea has no carriage returns. The DOM strips them on the way in, so
// what comes back out is always LF — and everything else in the browser that
// splits, highlights, renders or splices text assumes LF too.
//
// A checkout on Windows is CRLF throughout. Without this, opening a file there
// and changing one word would send the whole file back with different line
// endings: a one-line edit arriving as a total rewrite, and a fight with
// whatever the repository has decided about endings.
//
// So text is normalised on the way in and restored on the way out. The digest
// that guards the edit is taken of the bytes as they were on disk, before any
// of this — the precondition has to describe the real file, not the one the
// browser found comfortable.

export const CRLF = "\r\n";
export const LF = "\n";

// endingOf reports how a text ends its lines.
//
// A file with both is decided by which is commoner: it has to be written back
// one way or another, and the majority is the one that leaves the smaller diff.
export function endingOf(text) {
  const s = String(text ?? "");
  let crlf = 0;
  for (let i = s.indexOf(CRLF); i !== -1; i = s.indexOf(CRLF, i + 2)) crlf++;
  let lf = 0;
  for (let i = s.indexOf(LF); i !== -1; i = s.indexOf(LF, i + 1)) lf++;
  return crlf > lf - crlf ? CRLF : LF;
}

// toLF is what the browser works in.
export function toLF(text) {
  return String(text ?? "").split(CRLF).join(LF);
}

// fromLF puts back what toLF took away.
export function fromLF(text, ending) {
  const s = String(text ?? "");
  return ending === CRLF ? s.split(LF).join(CRLF) : s;
}
