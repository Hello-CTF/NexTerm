import { describe, expect, it } from "vitest";
import { fakeQuery } from "../demo/data";

describe("demo SQL 引导文案", () => {
  it("拦截 DDL/结构变更并直接说明原因，不引用外部文档章节", () => {
    const ddl = [
      "DROP TABLE orders",
      "alter table users add column age int",
      "create table t (id int)",
      "truncate table orders",
      "rename table orders to orders_old",
    ];
    for (const sql of ddl) {
      const result = fakeQuery(sql);
      expect(result.error, sql).toContain("演示模式");
      expect(result.error, sql).toContain("DDL");
      expect(result.error, sql).not.toContain("§");
      expect(result.error, sql).not.toContain("产品文档");
    }
  });

  it("查询与数据修改不受影响", () => {
    expect(fakeQuery("SELECT * FROM orders").error).toBeNull();
    expect(fakeQuery("select created_at from users").error).toBeNull();
    expect(fakeQuery("UPDATE orders SET status = 'paid' WHERE id = 1").error).toBeNull();
    expect(fakeQuery("DELETE FROM orders WHERE id = 1").error).toBeNull();
  });
});
