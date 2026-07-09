function UserList({ onSelect }) {
  fetchUsers();
  return <button onClick={onSelect}>Click</button>;
}
