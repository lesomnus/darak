// binaryNotice renders the "this is a binary file" message a text/code/edit
// view shows instead of decoding a binary as text. Mirrors VS Code refusing to
// display a binary; here it also protects the editor from a corrupting save.
export function binaryNotice(el: HTMLElement, verb: string): void {
  const p = document.createElement('p')
  p.className = 'muted preview-notice'
  p.textContent = `바이너리 파일이라 ${verb} 수 없습니다. 다운로드해서 여세요.`
  el.appendChild(p)
}
