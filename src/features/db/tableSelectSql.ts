// tableSelectSql is the query a table chip fills into the SQL editor. Free of imports so the website demo can mirror it.
export function tableSelectSql(dialect: string, schema: string, table: string): string {
  const quote = dialect === "postgres" ? '"' : "`";
  const name = [schema, table]
    .map((part) => `${quote}${part.replaceAll(quote, quote + quote)}${quote}`)
    .join(".");
  return `SELECT * FROM ${name} LIMIT 200;`;
}
