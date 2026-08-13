export type Toast = { id: string; kind: "info" | "success" | "error"; message: string };

export const notificationsStore = {
  toasts: () => [] as Toast[],
  add: (_: Toast) => {},
};
