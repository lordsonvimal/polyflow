//go:build ignore

package main

func clientCall(cc ClientConn) error {
	// Generated stub internals: Invoke carries the full gRPC path.
	return cc.Invoke(ctx, "/example.UserService/GetUser", req, &resp)
}

func setupServer(s Server) {
	// Generated registration helper: name follows Register<Svc>Server convention.
	pb.RegisterUserServiceServer(s, &userServiceImpl{})
}
