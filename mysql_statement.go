package mysql

import (
	"fmt"
	"os"
	"log"
)

const (
	ParamLimit	= 64
)

/**
 * Prepared statement struct
 */
type MySQLStatement struct {
	mysql		*MySQL

	prepared	bool

	StatementId	uint32

	Params		[]*MySQLParam
	ParamCount	uint16
	paramsRead	uint64
	paramsEOF	bool
	paramData	[]interface{}
	paramsBound	bool
	paramsRebound	bool

	result		*MySQLResult
	resExecuted	bool
}

/**
 * Prepare sql statement
 */
func (stmt *MySQLStatement) Prepare(sql string) bool {
	mysql := stmt.mysql
	if mysql.Logging { log.Stdout("Prepare statement called") }
	// Reset error/sequence vars
	mysql.reset()
	// Send command
	mysql.command(COM_STMT_PREPARE, sql)
	if mysql.Errno != 0 {
		return false
	}
	if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence - 1) + "] Sent prepare command to server") }
	// Get result packet(s)
	for {
		// Get result packet
		stmt.getPrepareResult()
		if mysql.Errno != 0 {
			return false
		}
		// If buffer is empty break loop
		if mysql.reader.Buffered() == 0 {
			break
		}
	}
	stmt.prepared = true
	return true
}

/**
 * Bind params
 */
func (stmt *MySQLStatement) BindParams(params ...interface{}) bool {
	mysql := stmt.mysql
	// Check statement has been prepared
	if !stmt.prepared {
		mysql.error(CR_NO_PREPARE_STMT, CR_NO_PREPARE_STMT_STR)
		return false
	}
	if mysql.Logging { log.Stdout("Bind params called") }
	// Check param count
	if uint16(len(params)) != stmt.ParamCount {
		return false
	}
	// Save params @todo this should send some packets as long packets
	stmt.paramData = params
	stmt.paramsBound = true
	stmt.paramsRebound = true
	return true
}

/**
 * Execute statement
 */
func (stmt *MySQLStatement) Execute() *MySQLResult {
	mysql := stmt.mysql
	var err os.Error
	// Check statement has been prepared
	if !stmt.prepared {
		mysql.error(CR_NO_PREPARE_STMT, CR_NO_PREPARE_STMT_STR)
		return nil
	}
	// Check params are bound
	if !stmt.paramsBound {
		mysql.error(CR_PARAMS_NOT_BOUND, CR_PARAMS_NOT_BOUND_STR)
		return nil
	}
	if mysql.Logging { log.Stdout("Execute statement called") }
	// Reset error/sequence vars
	mysql.reset()
	// Construct packet
	pkt := new(packetExecute)
	pkt.command        = COM_STMT_EXECUTE
	pkt.statementId    = stmt.StatementId
	pkt.flags          = CURSOR_TYPE_NO_CURSOR
	pkt.iterationCount = 1
	pkt.encodeNullBits(stmt.paramData)
	if stmt.paramsRebound {
		pkt.newParamBound = 1
	} else {
		pkt.newParamBound = 0
	}
	pkt.encodeParams(stmt.paramData)
	err = pkt.write(mysql.writer)
	if err != nil {
		mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
		return nil
	}
	mysql.sequence ++
	if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence - 1) + "] " + "Sent execute statement to server") }
	// Get result packet(s)
	for {
		// Get result packet
		stmt.getExecuteResult()
		if mysql.Errno != 0 {
			return nil
		}
		// If buffer is empty break loop
		if mysql.reader.Buffered() == 0 {
			break
		}
	}
	return stmt.result
}

/**
 * Close statement
 */
func (stmt *MySQLStatement) Close() bool {
	mysql := stmt.mysql
	// Check statement has been prepared
	if !stmt.prepared {
		mysql.error(CR_NO_PREPARE_STMT, CR_NO_PREPARE_STMT_STR)
		return false
	}
	if mysql.Logging { log.Stdout("Close statement called") }
	// Reset error/sequence vars
	mysql.reset()
	// Send command
	mysql.command(COM_STMT_CLOSE, stmt.StatementId)
	if mysql.Errno != 0 {
		return false
	}
	if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence - 1) + "] Sent close statement command to server") }
	return true
}

/**
 * Reset statement
 */
func (stmt *MySQLStatement) Reset() bool {
	mysql := stmt.mysql
	// Check statement has been prepared
	if !stmt.prepared {
		mysql.error(CR_NO_PREPARE_STMT, CR_NO_PREPARE_STMT_STR)
		return false
	}
	if mysql.Logging { log.Stdout("Reset statement called") }
	// Reset error/sequence vars
	mysql.reset()
	// Send command
	mysql.command(COM_STMT_RESET, stmt.StatementId)
	if mysql.Errno != 0 {
		return false
	}
	if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence - 1) + "] Sent reset statement command to server") }
	return true
}

/**
 * Function to read prepare result packets
 */
func (stmt *MySQLStatement) getPrepareResult() {
	mysql := stmt.mysql
	var err os.Error
	// Get header and validate header info
	hdr := new(packetHeader)
	err = hdr.read(mysql.reader)
	// Read error
	if err != nil {
		// Assume lost connection to server
		mysql.error(CR_SERVER_LOST, CR_SERVER_LOST_STR)
		return
	}
	// Check data length
	if int(hdr.length) > mysql.reader.Buffered() {
		mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
		return
	}
	// Check sequence number
	if hdr.sequence != mysql.sequence {
		mysql.error(CR_COMMANDS_OUT_OF_SYNC, CR_COMMANDS_OUT_OF_SYNC_STR)
		return
	}
	// Read the next byte to identify the type of packet
	c, err := mysql.reader.ReadByte()
	mysql.reader.UnreadByte()
	switch {
		// Unknown packet, read it and leave it for now
		default:
			bytes := make([]byte, hdr.length)
			mysql.reader.Read(bytes)
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received unknown packet from server with first byte: " + fmt.Sprint(c)) }
		// OK Packet 00
		case c == ResultPacketOK:
			pkt := new(packetOKPrepared)
			pkt.header = hdr
			err = pkt.read(mysql.reader)
			if err != nil {
				mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
			}
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received ok for prepared statement packet from server") }
			// Save statement info
			stmt.result = new(MySQLResult)
			stmt.StatementId  = pkt.statementId
			stmt.result.FieldCount   = uint64(pkt.columnCount)
			stmt.ParamCount   = pkt.paramCount
			stmt.result.WarningCount = pkt.warningCount
			// Initialise params/fields
			stmt.Params = make([]*MySQLParam, pkt.paramCount)
			stmt.paramData = make([]interface{}, pkt.paramCount)
			stmt.result.Fields = make([]*MySQLField, pkt.columnCount)
		// Error Packet ff
		case c == ResultPacketError:
			pkt := new(packetError)
			pkt.header = hdr
			err = pkt.read(mysql.reader)
			if err != nil {
				mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
			} else {
				mysql.error(int(pkt.errno), pkt.error)
			}
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received error packet from server") }
		// Making assumption that statement packets follow similar format to result packets
		// If param count > 0 then first will get parameter packets following EOF
		// After this should get standard field packets followed by EOF
		// Parameter packet
		case c >= 0x01 && c <= 0xfa && stmt.ParamCount > 0 && !stmt.paramsEOF:
			// This packet simply reads the number of bytes in the buffer per header length param
			// The packet specification for these packets is wrong also within MySQL code it states:
			// skip parameters data: we don't support it yet (in libmysql/libmysql.c)
			pkt := new(packetParameter)
			pkt.header = hdr
			err = pkt.read(mysql.reader)
			if err != nil {
				mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
			}
			// Increment params read
			stmt.paramsRead ++
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received param packet from server (ignored)") }
		// Field packet
		case c >= 0x01 && c <= 0xfa && stmt.result.FieldCount > 0 && !stmt.result.fieldsEOF:
			pkt := new(packetField)
			pkt.header = hdr
			err = pkt.read(mysql.reader)
			if err != nil {
				mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
				return
			}
			// Populate field data (ommiting anything which doesnt seam useful at time of writing)
			field := new(MySQLField)
			field.Name	    = pkt.name
			field.Length	    = pkt.length
			field.Type	    = pkt.fieldType
			field.Decimals	    = pkt.decimals
			field.Flags 	    = new(MySQLFieldFlags)
			field.Flags.process(pkt.flags)
			stmt.result.Fields[stmt.result.fieldsRead] = field
			// Increment fields read count
			stmt.result.fieldsRead ++
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received field packet from server") }
		// EOF Packet fe
		case c == ResultPacketEOF:
			pkt := new(packetEOF)
			pkt.header = hdr
			err = pkt.read(mysql.reader)
			if err != nil {
				mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
				return
			}
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received eof packet from server") }
			// Change EOF flag
			if stmt.ParamCount > 0 && !stmt.paramsEOF {
				stmt.paramsEOF = true
				if mysql.Logging { log.Stdout("End of param packets") }
			} else if stmt.result.fieldsEOF != true {
				stmt.result.fieldsEOF = true
				if mysql.Logging { log.Stdout("End of field packets") }
			}
	}
	// Increment sequence
	mysql.sequence ++
}

/**
 * Function to read execute result packets
 */
func (stmt *MySQLStatement) getExecuteResult() {
	mysql := stmt.mysql
	var err os.Error
	// Get header and validate header info
	hdr := new(packetHeader)
	err = hdr.read(mysql.reader)
	// Read error
	if err != nil {
		// Assume lost connection to server
		mysql.error(CR_SERVER_LOST, CR_SERVER_LOST_STR)
		return
	}
	// Check data length
	if int(hdr.length) > mysql.reader.Buffered() {
		mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
		return
	}
	// Check sequence number
	if hdr.sequence != mysql.sequence {
		mysql.error(CR_COMMANDS_OUT_OF_SYNC, CR_COMMANDS_OUT_OF_SYNC_STR)
		return
	}
	// Read the next byte to identify the type of packet
	c, err := mysql.reader.ReadByte()
	mysql.reader.UnreadByte()
	switch {
		// Unknown packet, read it and leave it for now
		default:
			bytes := make([]byte, hdr.length)
			mysql.reader.Read(bytes)
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received unknown packet from server with first byte: " + fmt.Sprint(c)) }
		// OK Packet 00
		case c == ResultPacketOK && !stmt.resExecuted:
			pkt := new(packetOK)
			pkt.header = hdr
			err = pkt.read(mysql.reader)
			if err != nil {
				mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
				return
			}
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received ok packet from server") }
			// Create result
			stmt.result = new(MySQLResult)
			stmt.result.AffectedRows = pkt.affectedRows
			stmt.result.InsertId 	 = pkt.insertId
			stmt.result.WarningCount = pkt.warningCount
			stmt.result.Message	 = pkt.message
			stmt.resExecuted = true
		// Error Packet ff
		case c == ResultPacketError:
			pkt := new(packetError)
			pkt.header = hdr
			err = pkt.read(mysql.reader)
			if err != nil {
				mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
			} else {
				mysql.error(int(pkt.errno), pkt.error)
			}
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received error packet from server") }
		// Result Set Packet 1-250 (first byte of Length-Coded Binary)
		case c >= 0x01 && c <= 0xfa && !stmt.resExecuted:
			pkt := new(packetResultSet)
			pkt.header = hdr
			err = pkt.read(mysql.reader)
			if err != nil {
				mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
				return
			}
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received result set packet from server") }
			// If fields sent again re-read incase for some reason something changed
			if pkt.fieldCount > 0 {
				stmt.result.FieldCount = pkt.fieldCount
				stmt.result.Fields     = make([]*MySQLField, pkt.fieldCount)
				stmt.result.fieldsRead = 0
				stmt.result.fieldsEOF  = false
			}
			stmt.resExecuted = true
		// Field Packet 1-250 ("")
		case c >= 0x01 && c <= 0xfa && stmt.result.FieldCount > stmt.result.fieldsRead && !stmt.result.fieldsEOF:
			pkt := new(packetField)
			pkt.header = hdr
			err = pkt.read(mysql.reader)
			if err != nil {
				mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
				return
			}
			// Populate field data (ommiting anything which doesnt seam useful at time of writing)
			field := new(MySQLField)
			field.Name	    = pkt.name
			field.Length	    = pkt.length
			field.Type	    = pkt.fieldType
			field.Decimals	    = pkt.decimals
			field.Flags 	    = new(MySQLFieldFlags)
			field.Flags.process(pkt.flags)
			stmt.result.Fields[stmt.result.fieldsRead] = field
			// Increment fields read count
			stmt.result.fieldsRead ++
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received field packet from server") }
		// Binary row packets appear to always start 00
		// EOF Packet fe
		case c == ResultPacketEOF:
			pkt := new(packetEOF)
			pkt.header = hdr
			err = pkt.read(mysql.reader)
			if err != nil {
				mysql.error(CR_MALFORMED_PACKET, CR_MALFORMED_PACKET_STR)
				return
			}
			if mysql.Logging { log.Stdout("[" + fmt.Sprint(mysql.sequence) + "] Received eof packet from server") }
			// Change EOF flag
			if stmt.result.FieldCount > 0 && !stmt.result.fieldsEOF {
				stmt.result.fieldsEOF = true
				if mysql.Logging { log.Stdout("End of field packets") }
			} else if !stmt.result.rowsEOF {
				stmt.result.rowsEOF = true
				if mysql.Logging { log.Stdout("End of row packets") }
			}
	}
	// Increment sequence
	mysql.sequence ++
}

/**
 * Param definition
 */
type MySQLParam struct {
	Type		[]byte
	Flags		uint16
	Decimals	uint8
	Length		uint32
}
