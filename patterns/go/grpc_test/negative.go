//go:build ignore

package negative

// Method named "Call", not "Invoke" — must not match grpc_client_call.
func example1(cc ClientConn) error {
	return cc.Call(ctx, "/example.Service/Method", req, &resp)
}

// Second argument is an identifier (not a string literal) — Invoke with
// a variable path must not match (we only capture literal service paths).
func example2(cc ClientConn, method string) error {
	return cc.Invoke(ctx, method, req, &resp)
}

// Register prefix but no "Server" suffix — must not match grpc_server_register.
func example3(s Server) {
	pb.RegisterUserService(s, &impl{})
}

// 3-argument Register*Server (extra arg) — must not match the 2-arg pattern.
func example4(s Server, impl Impl, extra string) {
	pb.RegisterUserServiceServer(s, impl, extra)
}
