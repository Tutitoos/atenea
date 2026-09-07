abstract class Archive {
  String? fetch(String key);
}

class MemoryArchive implements Archive {
  MemoryArchive(this.items);

  final Map<String, String> items;

  @override
  String? fetch(String key) => items[key];
}
