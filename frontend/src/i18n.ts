let table: Record<string, string> = {};

export function setMessages(m: Record<string, string>): void {
  table = m;
}

/** 取訊息；缺鍵回傳 key 本身（與 Go 端 i18n.T 一致）。 */
export function t(key: string): string {
  return table[key] ?? key;
}
