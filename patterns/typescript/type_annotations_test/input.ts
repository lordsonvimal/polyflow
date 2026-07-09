type UserID = number;
type Result<T> = { data: T; error: string | null };
const wrapped: Result<number> = { data: 1, error: null };
