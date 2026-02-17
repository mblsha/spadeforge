module top;
  (* keep = "true" *) wire a;
  (* keep = "true" *) wire b;
  assign a = 1'b0;
  assign b = ~a;
endmodule
