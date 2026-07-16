// Different function names — must not match useQuery / useMutation.
fetchData(GET_BOOKS_QUERY);
submitMutation(CREATE_BOOK_MUTATION);
getData(QUERY);

// Object without Query/Mutation/Subscription key — must not match graphql_resolver.
const handlers = {
  click: () => {},
  submit: (_, args) => args,
};

// Flat resolver (no nesting under Query/Mutation) — must not match.
const flatResolvers = {
  books: () => [],
};
