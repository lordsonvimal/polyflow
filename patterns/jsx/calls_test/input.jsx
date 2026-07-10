function UserList({ onSelect }) {
  fetchUsers();
  return (
    <div>
      <button onClick={onSelect}>Click</button>
      <button on:click={onSelect}>Delegated</button>
    </div>
  );
}
