// GraphQL client (Apollo Client hook calls).
const { data: booksData } = useQuery(GET_BOOKS_QUERY);
const [createBook] = useMutation(CREATE_BOOK_MUTATION);

// GraphQL server resolver map — one match per operation field.
const resolvers = {
  Query: {
    books: () => [],
  },
  Mutation: {
    createBook: (_, args) => args,
  },
};
