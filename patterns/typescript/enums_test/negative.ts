interface Status {
  code: number;
}
const STATES = { open: 1, closed: 2 } as const;
