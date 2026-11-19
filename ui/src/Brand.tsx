import logo from "./assets/brand/logo.svg?no-inline";

/** Drop mark and wordmark. */
export default function Brand() {
  return (
    <div className="product-brand">
      <img src={logo} width="32" height="32" alt="" />
      <span>Drop</span>
    </div>
  );
}
