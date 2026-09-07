export interface Repository {
  get(id: string): string | undefined;
}

export class MemoryRepository implements Repository {
  constructor(private readonly items: Record<string, string>) {}

  get(id: string): string | undefined {
    return this.items[id];
  }
}
