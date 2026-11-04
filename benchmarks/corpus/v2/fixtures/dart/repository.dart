abstract class Repository {
  String? get(String id);
}

class MemoryRepository implements Repository {
  MemoryRepository(this.items);

  final Map<String, String> items;

  @override
  String? get(String id) => items[id];
}
