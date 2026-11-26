import fs from "fs";
import * as path from "node:path";
import { a as b, Helper } from "./lib/util";
import type { OnlyType } from "./types";

interface Shape { area(): number }

export class Base {
  protected log() { console.log("x"); }
}

export class Foo extends Base implements Shape {
  private count = 0;
  handler = () => { this.log(); };
  constructor(private name: string) { super(); }
  area(): number { return this.helper() + b(1); }
  static make(): Foo { return new Foo("a"); }
  private helper(): number {
    path.join("a", "b");
    return top();
  }
}

export enum Color { Red, Green }

namespace Geo {
  export function dist(): number { return fs.readFileSync("x").length; }
}

export const top = (): number => {
  const inner = () => new Helper().run();
  return inner();
};

export default function main() { return top(); }

async function load() { const m = await import("./dyn"); return m; }
