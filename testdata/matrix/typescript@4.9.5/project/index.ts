function identity<T>(value: T): T {
  return value;
}

function transform<T, U>(val: T, fn: (x: T) => U): U {
  return fn(val);
}

function main(): void {
  const result = transform("hello", identity);
}

main();
