interface User {
  id: number;
  name: string;
}
interface Admin extends User {
  role: string;
}
