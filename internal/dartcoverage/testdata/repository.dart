abstract class Catalog {
  String? lookup(String key);
}

class MemoryCatalog implements Catalog {
  MemoryCatalog(this.items);

  final Map<String, String> items;

  @override
  String? lookup(String key) => items[key];
}
