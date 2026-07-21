def greet(name)
  "Hello, #{name}"
end

def process(items)
  items.map { |item| greet(item) }
end

def main
  process(["Alice", "Bob"])
end

main
