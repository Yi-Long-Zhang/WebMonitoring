// Saves a body the API returned as a file. This lives here rather than in the
// calling component because it is DOM plumbing, not view logic -- the layout
// only decides what to export and what to call it.
export function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  // Revoking in the same tick can cancel the download before it starts, and
  // keeping the object URL alive past the click leaks the blob.
  setTimeout(() => URL.revokeObjectURL(url), 0);
}
