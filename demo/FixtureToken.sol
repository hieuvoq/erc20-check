// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

// Local measurement fixture. Fees are burned and the deployer configures the test cases.
contract FixtureToken {
    address public immutable owner = msg.sender;
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;
    address public taxedPair;
    uint256 public delegatedLimit = type(uint256).max;
    uint16 public buyFee;
    uint16 public sellFee;
    uint16 public walletFee;
    event Transfer(address indexed from, address indexed to, uint256 value);
    event Approval(address indexed owner, address indexed spender, uint256 value);

    constructor() {
        balanceOf[msg.sender] = 1_000_000 ether;
    }

    function configure(address pair, uint16 buy, uint16 sell, uint16 wallet) external {
        require(msg.sender == owner && buy <= 1000 && sell <= 1000 && wallet <= 1000);
        taxedPair = pair;
        buyFee = buy;
        sellFee = sell;
        walletFee = wallet;
    }

    function setDelegatedLimit(uint256 amount) external {
        require(msg.sender == owner);
        delegatedLimit = amount;
    }

    function approve(address spender, uint256 amount) external returns (bool) {
        allowance[msg.sender][spender] = amount;
        emit Approval(msg.sender, spender, amount);
        return true;
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        move(msg.sender, to, amount);
        return true;
    }

    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        require(amount <= delegatedLimit, "per-call allowance limit");
        allowance[from][msg.sender] -= amount;
        move(from, to, amount);
        return true;
    }

    function move(address from, address to, uint256 amount) private {
        uint256 rate = from == taxedPair
            ? buyFee
            : to == taxedPair ? sellFee : (from.code.length == 0 && to.code.length == 0 ? walletFee : 0);
        uint256 fee = (amount * rate + 9999) / 10000;
        balanceOf[from] -= amount;
        balanceOf[to] += amount - fee;
        emit Transfer(from, to, amount - fee);
        if (fee != 0) emit Transfer(from, address(0), fee);
    }
}
